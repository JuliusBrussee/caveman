// Package nativehook owns hot host-hook transport. Frequent pre-tool no-op
// events stay in this small Go process; richer lifecycle events delegate to the
// generated Node adapter until their host-specific output contracts move here.
package nativehook

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/JuliusBrussee/caveman/proxy/internal/gitsafe"
	"github.com/JuliusBrussee/caveman/proxy/internal/nativeruntime"
)

const maxPayloadBytes = 2 * 1024 * 1024

type hostEvent map[string]any

// Run processes one host callback. Adapter failures deliberately emit nothing
// and return nil: host command continues unchanged.
func Run(ctx context.Context, home, agent, adapterPath string, raw []byte, stdout, stderr io.Writer) error {
	if len(raw) == 0 || len(raw) > maxPayloadBytes || !validAgent(agent) {
		return nil
	}
	var event hostEvent
	if err := json.Unmarshal(raw, &event); err != nil || event == nil {
		return nil
	}
	eventName := normalizeEvent(agent, firstString(event, "hook_event_name", "event_name", "event"))
	if eventName != "PreToolUse" && eventName != "PermissionRequest" {
		delegate(ctx, adapterPath, agent, raw, stdout, stderr)
		return nil
	}
	sessionID := bounded(firstString(event, "session_id", "sessionId"), 160)
	if sessionID == "" {
		return nil
	}
	request := preToolRequest(ctx, home, agent, eventName, sessionID, event)
	response, err := nativeruntime.Call(ctx, home, request)
	if err != nil {
		recordFallback(home, agent, eventName, sessionID, request.Tool.Name, raw)
		return nil
	}
	if request.PolicyMode == "record" || response.Context == "" {
		return nil
	}
	return json.NewEncoder(stdout).Encode(map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName":     eventName,
			"additionalContext": response.Context,
		},
	})
}

func validAgent(agent string) bool {
	switch agent {
	case "claude", "codex", "hermes", "gemini", "opencode", "pi":
		return true
	default:
		return false
	}
}

func normalizeEvent(agent, value string) string {
	if agent == "gemini" {
		switch value {
		case "BeforeAgent":
			return "UserPromptSubmit"
		case "BeforeTool":
			return "PreToolUse"
		case "AfterTool":
			return "PostToolUse"
		case "BeforeModel":
			return "ModelBefore"
		case "AfterModel":
			return "ModelAfter"
		case "PreCompress":
			return "PreCompact"
		case "AfterAgent":
			return "Stop"
		}
	}
	return value
}

func preToolRequest(ctx context.Context, home, agent, eventName, sessionID string, event hostEvent) nativeruntime.Request {
	cwd := bounded(firstString(event, "cwd", "working_directory", "workingDirectory"), 4096)
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	toolName := bounded(firstString(event, "tool_name", "toolName"), 160)
	tool := &nativeruntime.Tool{Name: toolName}
	claimedRepositoryState := bounded(firstString(event, "repository_state", "repositoryState"), 512)
	repositoryState := claimedRepositoryState
	for _, key := range []string{"tool_input", "toolInput", "args"} {
		if input, ok := event[key]; ok {
			if encoded, err := json.Marshal(input); err == nil {
				tool.Input = encoded
				tool.InputState = fileInputState(cwd, input)
				if toolNeedsRepositoryState(toolName, input) {
					if local := currentRepositoryState(ctx, cwd); local != "" {
						repositoryState = local
					}
				}
			}
			break
		}
	}
	mode, profile := nativePolicy(home)
	return nativeruntime.Request{
		ProtocolVersion: nativeruntime.ProtocolVersion,
		PolicyMode:      mode,
		Profile:         profile,
		Agent: nativeruntime.Agent{
			ID:      agent,
			Version: bounded(firstString(event, "agent_version", "version"), 160),
			Surface: bounded(firstString(event, "surface", "platform"), 160),
		},
		Session: nativeruntime.Session{
			ID:              agent + ":" + sessionID,
			HostSessionID:   sessionID,
			ParentSessionID: bounded(firstString(event, "parent_session_id", "parentSessionId"), 160),
			CWD:             cwd,
			RepositoryState: repositoryState,
		},
		Event: nativeruntime.Event{Type: "tool.before", TimestampMS: time.Now().UnixMilli()},
		Tool:  tool,
	}
}

func toolNeedsRepositoryState(name string, input any) bool {
	lower := strings.ToLower(name)
	if strings.Contains(lower, "search") || strings.Contains(lower, "grep") || lower == "rg" || strings.Contains(lower, "find") {
		return true
	}
	values, ok := input.(map[string]any)
	if !ok {
		return false
	}
	command, _ := values["command"].(string)
	if command == "" {
		command, _ = values["cmd"].(string)
	}
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return false
	}
	return fields[0] == "pytest" || fields[0] == "test" || fields[0] == "build" || (fields[0] == "go" && len(fields) > 1 && (fields[1] == "test" || fields[1] == "build"))
}

func currentRepositoryState(ctx context.Context, cwd string) string {
	if cwd == "" {
		return ""
	}
	statusCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	// `status` refreshes the index, which runs core.fsmonitor — this is the
	// hottest git call we make against a repository we do not trust. Disabling
	// fsmonitor costs the people who configured it BECAUSE their repo is huge:
	// if status then misses the 100ms budget above, repository state comes back
	// empty and reuse silently stops. That degrades safely, and moving the
	// budget is a latency decision for every user, so it needs a measurement
	// this fix does not have.
	cmd := gitsafe.Command(statusCtx, cwd, "status", "--porcelain=v1", "-z", "--branch", "--untracked-files=all")
	var status bytes.Buffer
	cmd.Stdout = &status
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil || status.Len() > 8*1024*1024 {
		return ""
	}
	gitDir := findGitDir(cwd)
	if gitDir == "" {
		return ""
	}
	hash := sha256.New()
	realCWD, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		return ""
	}
	_, _ = hash.Write([]byte(realCWD + "\x00status\x00"))
	_, _ = hash.Write(status.Bytes())
	readBytes := int64(0)
	include := func(label, path string) bool {
		_, _ = fmt.Fprintf(hash, "\x00%s\x00%s\x00", label, path)
		info, err := os.Lstat(path)
		if err != nil {
			_, _ = hash.Write([]byte("missing"))
			return true
		}
		if !info.Mode().IsRegular() {
			_, _ = fmt.Fprintf(hash, "mode:%d:size:%d", info.Mode(), info.Size())
			return true
		}
		if readBytes+info.Size() > 8*1024*1024 {
			return false
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return false
		}
		readBytes += int64(len(data))
		_, _ = hash.Write(data)
		return true
	}
	if !include("index", filepath.Join(gitDir, "index")) {
		return ""
	}
	for _, field := range bytes.Split(status.Bytes(), []byte{0}) {
		if len(field) == 0 || bytes.HasPrefix(field, []byte("## ")) {
			continue
		}
		relative := string(field)
		if len(field) >= 4 && field[2] == ' ' {
			relative = string(field[3:])
		}
		if relative == "" || !include("changed", filepath.Join(cwd, filepath.FromSlash(relative))) {
			return ""
		}
	}
	return "git:sha256:" + hex.EncodeToString(hash.Sum(nil))
}

func findGitDir(cwd string) string {
	current, err := filepath.Abs(cwd)
	if err != nil {
		return ""
	}
	for {
		candidate := filepath.Join(current, ".git")
		info, statErr := os.Stat(candidate)
		if statErr == nil && info.IsDir() {
			return candidate
		}
		if statErr == nil && info.Mode().IsRegular() {
			data, readErr := os.ReadFile(candidate)
			line := strings.TrimSpace(string(data))
			if readErr == nil && strings.HasPrefix(line, "gitdir:") {
				path := strings.TrimSpace(strings.TrimPrefix(line, "gitdir:"))
				if !filepath.IsAbs(path) {
					path = filepath.Join(current, path)
				}
				return filepath.Clean(path)
			}
		}
		parent := filepath.Dir(current)
		if parent == current {
			return ""
		}
		current = parent
	}
}

func nativePolicy(home string) (string, string) {
	profiles := map[string]bool{"record-only": true, "core": true, "core-lean-build": true, "ledger": true, "ccr-masking": true, "cache-aware": true, "full-safe": true, "full-max": true}
	if explicit := strings.ToLower(strings.TrimSpace(os.Getenv("CAVEMAN_NATIVE_PROFILE"))); explicit != "" {
		if !profiles[explicit] || explicit == "record-only" {
			return "record", "record-only"
		}
		mode := "safe"
		if explicit == "full-max" {
			mode = "max"
		}
		return mode, explicit
	}
	if explicit := strings.ToLower(strings.TrimSpace(os.Getenv("CAVEMAN_NATIVE_MODE"))); explicit != "" {
		switch explicit {
		case "safe":
			return "safe", "full-safe"
		case "max":
			return "max", "full-max"
		default:
			return "record", "record-only"
		}
	}
	mode := strings.ToLower(strings.TrimSpace(os.Getenv("CAVEMAN_WRAP_MODE")))
	if mode == "" {
		mode = configuredMode(home)
	}
	switch mode {
	case "compress":
		return "safe", "full-safe"
	case "pixel":
		return "max", "full-max"
	default:
		return "record", "record-only"
	}
}

func configuredMode(home string) string {
	userHome := os.Getenv("HOME")
	if userHome == "" {
		userHome = filepath.Dir(home)
	}
	raw, err := os.ReadFile(filepath.Join(userHome, ".caveman-cloud", "config.json"))
	if err != nil {
		return "compress"
	}
	var config map[string]any
	if json.Unmarshal(raw, &config) != nil {
		return "record"
	}
	for _, group := range []string{"think", "wrap"} {
		if block, ok := config[group].(map[string]any); ok {
			if mode, ok := block["mode"].(string); ok {
				if mode == "compress" || mode == "pixel" || mode == "record" {
					return mode
				}
				return "record"
			}
		}
	}
	return "compress"
}

func fileInputState(cwd string, input any) string {
	values, ok := input.(map[string]any)
	if !ok || cwd == "" {
		return ""
	}
	path := ""
	for _, key := range []string{"path", "file_path", "file", "filename"} {
		if value, ok := values[key].(string); ok && strings.TrimSpace(value) != "" {
			path = value
			break
		}
	}
	if path == "" || strings.ContainsRune(path, 0) {
		return ""
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(cwd, path)
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return ""
	}
	info, err := os.Stat(real)
	if err != nil {
		return ""
	}
	identity, _ := json.Marshal(map[string]any{"path": real, "size": info.Size(), "mtime_ms": float64(info.ModTime().UnixNano()) / 1e6, "mode": uint32(info.Mode())})
	sum := sha256.Sum256(identity)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func recordFallback(home, agent, eventName, sessionID, toolName string, raw []byte) {
	runtimeDir := filepath.Join(home, "runtime")
	if os.MkdirAll(runtimeDir, 0o700) != nil {
		return
	}
	sum := sha256.Sum256(raw)
	entry := map[string]any{
		"protocol_version": 1,
		"recorded_at":      time.Now().UTC().Format(time.RFC3339Nano),
		"agent":            agent,
		"event":            eventName,
		"host_session_id":  sessionID,
		"payload_bytes":    len(raw),
		"payload_sha256":   "sha256:" + hex.EncodeToString(sum[:]),
	}
	if toolName != "" {
		entry["tool_name"] = toolName
	}
	encoded, _ := json.Marshal(entry)
	path := filepath.Join(runtimeDir, "native-events.jsonl")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer file.Close()
	_, _ = file.Write(append(encoded, '\n'))
	_ = os.Chmod(path, 0o600)
}

func delegate(ctx context.Context, adapterPath, agent string, raw []byte, stdout, stderr io.Writer) {
	if adapterPath == "" {
		return
	}
	node := os.Getenv("NODE")
	if node == "" {
		node = "node"
	}
	cmd := exec.CommandContext(ctx, node, adapterPath, "native-hook", agent)
	cmd.Stdin = bytes.NewReader(raw)
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	if cmd.Run() != nil {
		return
	}
	_, _ = stdout.Write(out.Bytes())
	_, _ = stderr.Write(errOut.Bytes())
}

func firstString(event hostEvent, keys ...string) string {
	for _, key := range keys {
		if value, ok := event[key].(string); ok {
			return value
		}
	}
	return ""
}

func bounded(value string, max int) string {
	value = strings.TrimSpace(strings.NewReplacer("\r", " ", "\n", " ", "\x00", " ").Replace(value))
	if len(value) > max {
		value = value[:max]
	}
	return value
}
