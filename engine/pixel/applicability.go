// Ported from pxpipe (https://github.com/teamchong/pxpipe), MIT License, Copyright (c) 2026 claude-image-proxy contributors.

package pixel

import (
	"os"
	"regexp"
	"strings"
)

var variantTagRE = regexp.MustCompile(`\[[^\]]*\]`)

func AllowedModelBases() []string {
	raw, ok := os.LookupEnv("CAVE_PIXEL_MODELS")
	if !ok || strings.TrimSpace(raw) == "" {
		return []string{"claude-fable-5", "gpt-5.6"}
	}
	trimmed := strings.TrimSpace(raw)
	if falseyModelList(trimmed) {
		return []string{}
	}
	parts := strings.Split(trimmed, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if s := strings.TrimSpace(part); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func Allowed(model string) bool {
	if model == "" {
		return false
	}
	for _, candidate := range modelCandidates(variantTagRE.ReplaceAllString(model, "")) {
		for _, base := range AllowedModelBases() {
			if candidate == base || strings.HasPrefix(candidate, base+"-") {
				return true
			}
		}
	}
	return false
}

func falseyModelList(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "0", "false", "no", "off", "none":
		return true
	default:
		return false
	}
}

func modelCandidates(model string) []string {
	segs := strings.FieldsFunc(model, func(r rune) bool { return r == '.' || r == '/' })
	out := make([]string, 0, 1+len(segs)*4)
	out = appendModelCandidate(out, model)
	for i := range segs {
		if segs[i] == "" {
			continue
		}
		out = appendModelCandidate(out, strings.Join(segs[i:], "."))
		out = appendModelCandidate(out, strings.Join(segs[i:], "/"))
		if i == len(segs)-1 {
			out = appendModelCandidate(out, segs[i])
		}
	}
	return out
}

func appendModelCandidate(out []string, candidate string) []string {
	if candidate == "" {
		return out
	}
	out = append(out, candidate)
	if base, _, ok := strings.Cut(candidate, "@"); ok && base != "" {
		out = append(out, base)
	}
	return out
}
