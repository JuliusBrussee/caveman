//go:build integration

package browse

import (
	"bytes"
	"context"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/JuliusBrussee/caveman/engine"
	"github.com/JuliusBrussee/caveman/engine/ccr"
)

type recordingDriver struct {
	*CDPDriver
	lastRaw []byte
}

func (d *recordingDriver) Snapshot(ctx context.Context, pageURL string, wait time.Duration) ([]byte, error) {
	raw, err := d.CDPDriver.Snapshot(ctx, pageURL, wait)
	if err == nil {
		d.lastRaw = append(d.lastRaw[:0], raw...)
	}
	return raw, err
}

func TestCDPActionabilityRejectsDisabledButton(t *testing.T) {
	store, err := ccr.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	driver, err := NewCDPDriver(context.Background(), CDPOptions{BrowserPath: os.Getenv("CAVEMAN_BROWSE_CHROME"), Headless: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = driver.Close() })
	session := NewSession(engine.New(store, nil), driver, nil)

	url := `data:text/html,` + urlEscape(`<main><button disabled>Disabled save</button><button id="ok" onclick="window.clicked=true">Save settings</button></main>`)
	if _, err := driver.Snapshot(context.Background(), url, 100*time.Millisecond); err != nil {
		t.Fatalf("direct CDP snapshot failed: %v", err)
	}
	// Snapshot again through the MCP surface. This guards driver reuse: browser
	// lifetime belongs to the driver context, not the first request context.
	tr := session.snapshotTool(jsonArgs(t, map[string]any{"url": url, "wait": 100}))
	if tr.IsError {
		t.Fatalf("snapshot failed: %s", tr.Content[0].Text)
	}
	var payload snapshotPayload
	decodeToolText(t, tr, &payload)
	disabledUID := uidByName(t, payload.UIDs, "Disabled save")
	target, ok := session.lookupTarget(disabledUID)
	if !ok {
		t.Fatalf("missing target for disabled uid %q", disabledUID)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 750*time.Millisecond)
	defer cancel()
	if res, err := driver.Act(ctx, ActionRequest{Action: "click", UID: disabledUID}, target); err == nil || res.OK {
		t.Fatalf("disabled target should not be actionable: res=%+v err=%v", res, err)
	}
}

func TestCDPFullTokenEfficientReadActVerifyRecoverLoop(t *testing.T) {
	store, err := ccr.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	base, err := NewCDPDriver(context.Background(), CDPOptions{BrowserPath: os.Getenv("CAVEMAN_BROWSE_CHROME"), Headless: true})
	if err != nil {
		t.Fatal(err)
	}
	driver := &recordingDriver{CDPDriver: base}
	t.Cleanup(func() { _ = driver.Close() })
	session := NewSession(engine.New(store, nil), driver, nil)

	html, err := os.ReadFile("testdata/agent_checkout.html")
	if err != nil {
		t.Fatal(err)
	}
	pageURL := "data:text/html," + url.PathEscape(string(html))
	fullResult := session.snapshotTool(jsonArgs(t, map[string]any{"url": pageURL, "wait": 50}))
	if fullResult.IsError {
		t.Fatalf("full checkout snapshot failed: %s", fullResult.Content[0].Text)
	}
	var full snapshotPayload
	decodeToolText(t, fullResult, &full)
	first := session.snapshotTool(jsonArgs(t, map[string]any{"query": "Email Plan Save order"}))
	if first.IsError {
		t.Fatalf("initial snapshot failed: %s", first.Content[0].Text)
	}
	var initial snapshotPayload
	decodeToolText(t, first, &initial)
	if initial.TokensAfter >= initial.TokensBefore || initial.ViewTokens >= initial.TokensAfter {
		t.Fatalf("initial agent-visible snapshot was not efficient: %+v", initial)
	}
	t.Logf("checkout tokens raw=%d full-view=%d full-delivered=%d focused-view=%d focused-delivered=%d",
		initial.TokensBefore, full.ViewTokens, full.TokensAfter, initial.ViewTokens, initial.TokensAfter)
	emailUID := uidByName(t, initial.UIDs, "Email")
	planUID := uidByName(t, initial.UIDs, "Plan")
	saveUID := uidByName(t, initial.UIDs, "Save order")

	for _, action := range []map[string]any{
		{"action": "type", "uid": emailUID, "text": "ops@example.com"},
		{"action": "select", "uid": planUID, "option": "pro"},
		// Save sits below the fold. Success proves click auto-scrolls before
		// visibility and hit-testing instead of timing out on an offscreen box.
		{"action": "click", "uid": saveUID},
	} {
		result := session.actTool(jsonArgs(t, action))
		if result.IsError {
			t.Fatalf("action failed: args=%v result=%s", action, result.Content[0].Text)
		}
		var dispatched ActionResult
		decodeToolText(t, result, &dispatched)
		if !dispatched.OK || dispatched.Settled || !strings.Contains(dispatched.Note, "resnapshot") {
			t.Fatalf("action must report dispatch without inventing settled state: %+v", dispatched)
		}
	}

	final := session.snapshotTool(jsonArgs(t, map[string]any{"query": "Saved ops@example.com pro"}))
	if final.IsError {
		t.Fatalf("verification snapshot failed: %s", final.Content[0].Text)
	}
	var verified snapshotPayload
	decodeToolText(t, final, &verified)
	for _, want := range []string{"Saved", "ops@example.com", "pro"} {
		if !strings.Contains(verified.UIDs, want) {
			t.Fatalf("verification snapshot missing %q:\n%s", want, verified.UIDs)
		}
	}
	if strings.Contains(final.Content[0].Text, "verified") {
		t.Fatal("browse path must remain inferred-only")
	}

	recovered := session.recoverTool(jsonArgs(t, map[string]any{"recovery_handle": *verified.RecoveryHandle}))
	if recovered.IsError || !bytes.Equal([]byte(recovered.Content[0].Text), driver.lastRaw) {
		t.Fatal("live browser snapshot did not recover byte-exact raw AX bytes")
	}
}

func TestCDPStaleUIDFailsClosedAfterDOMReplacement(t *testing.T) {
	store, err := ccr.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	driver, err := NewCDPDriver(context.Background(), CDPOptions{BrowserPath: os.Getenv("CAVEMAN_BROWSE_CHROME"), Headless: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = driver.Close() })
	session := NewSession(engine.New(store, nil), driver, nil)
	pageURL := "data:text/html," + url.PathEscape(`<main><button id="swap">Replace me</button></main>`)
	snapshot := session.snapshotTool(jsonArgs(t, map[string]any{"url": pageURL, "wait": 50}))
	if snapshot.IsError {
		t.Fatal(snapshot.Content[0].Text)
	}
	var payload snapshotPayload
	decodeToolText(t, snapshot, &payload)
	uid := uidByName(t, payload.UIDs, "Replace me")
	if _, err := driver.Eval(context.Background(), `document.getElementById("swap").outerHTML='<button id="swap">Replacement</button>'`); err != nil {
		t.Fatal(err)
	}
	target, ok := session.lookupTarget(uid)
	if !ok {
		t.Fatal("snapshot did not cache stale-test target")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 750*time.Millisecond)
	defer cancel()
	if result, err := driver.Act(ctx, ActionRequest{Action: "click", UID: uid}, target); err == nil || result.OK {
		t.Fatalf("detached backend node id must fail closed: result=%+v err=%v", result, err)
	}
}

func TestCDPQueryScalesOnTwoHundredRowDashboard(t *testing.T) {
	store, err := ccr.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	driver, err := NewCDPDriver(context.Background(), CDPOptions{BrowserPath: os.Getenv("CAVEMAN_BROWSE_CHROME"), Headless: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = driver.Close() })
	session := NewSession(engine.New(store, nil), driver, nil)
	html, err := os.ReadFile("testdata/order_dashboard.html")
	if err != nil {
		t.Fatal(err)
	}
	pageURL := "data:text/html," + url.PathEscape(string(html))
	fullResult := session.snapshotTool(jsonArgs(t, map[string]any{"url": pageURL, "wait": 100}))
	if fullResult.IsError {
		t.Fatalf("full dashboard snapshot failed: %s", fullResult.Content[0].Text)
	}
	var full snapshotPayload
	decodeToolText(t, fullResult, &full)

	focusedResult := session.snapshotTool(jsonArgs(t, map[string]any{"query": "ORD-0173"}))
	if focusedResult.IsError {
		t.Fatalf("focused dashboard snapshot failed: %s", focusedResult.Content[0].Text)
	}
	var focused snapshotPayload
	decodeToolText(t, focusedResult, &focused)
	if focused.TokensAfter*10 > full.TokensAfter {
		t.Fatalf("focused view did not cut delivery by >=10x: full=%d focused=%d", full.TokensAfter, focused.TokensAfter)
	}
	if focused.TokensAfter > 160 || focused.Ratio < 0.99 {
		t.Fatalf("focused dashboard exceeded budget: %+v", focused)
	}
	uid := uidByName(t, focused.UIDs, "Review ORD-0173")
	if _, ok := session.lookupTarget(uid); !ok {
		t.Fatalf("focused result lost target %q", uid)
	}
	t.Logf("200-row dashboard tokens raw=%d full-view=%d full-delivered=%d focused-view=%d focused-delivered=%d",
		focused.TokensBefore, full.ViewTokens, full.TokensAfter, focused.ViewTokens, focused.TokensAfter)
}

func uidByName(t *testing.T, snapshot string, name string) string {
	t.Helper()
	for _, line := range strings.Split(snapshot, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "[") || !strings.Contains(line, strconv.Quote(name)) {
			continue
		}
		if end := strings.IndexByte(line, ']'); end > 1 {
			return line[1:end]
		}
	}
	t.Fatalf("no uid found for name %q in:\n%s", name, snapshot)
	return ""
}

func urlEscape(s string) string {
	replacer := strings.NewReplacer(
		"%", "%25", " ", "%20", "<", "%3C", ">", "%3E", "\"", "%22",
		"#", "%23", "\n", "",
	)
	return replacer.Replace(s)
}
