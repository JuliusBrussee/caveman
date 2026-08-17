package browse

import (
	"context"
	"testing"
	"time"

	"github.com/chromedp/cdproto/runtime"
	"github.com/go-json-experiment/json/jsontext"
)

// TestDecodeBoolObjectGuardsNilAndDecodes covers the issue #140 nil-object
// crash: CallFunctionOn can return a nil RemoteObject without an error, and the
// old callBoolOnNode dereferenced res.Value directly — a nil panic that, via the
// MCP panic hole, would take down the process. decodeBoolObject must fail closed
// on nil while still decoding real boolean results. Reachable without Chrome.
func TestDecodeBoolObjectGuardsNilAndDecodes(t *testing.T) {
	if _, err := decodeBoolObject(nil); err == nil {
		t.Fatal("nil result object must fail closed, not panic")
	}
	if v, err := decodeBoolObject(&runtime.RemoteObject{Value: jsontext.Value(`true`)}); err != nil || !v {
		t.Fatalf("true decode = %v, %v", v, err)
	}
	if v, err := decodeBoolObject(&runtime.RemoteObject{Value: jsontext.Value(`false`)}); err != nil || v {
		t.Fatalf("false decode = %v, %v", v, err)
	}
	if _, err := decodeBoolObject(&runtime.RemoteObject{Value: jsontext.Value(`"nope"`)}); err == nil {
		t.Fatal("non-boolean value must return a decode error")
	}
	if _, err := decodeBoolObject(&runtime.RemoteObject{}); err == nil {
		t.Fatal("empty value must return a decode error, not a false positive")
	}
}

func TestRequestContextCancelsWithParent(t *testing.T) {
	d := &CDPDriver{ctx: context.Background()}
	parent, cancelParent := context.WithCancel(context.Background())
	runCtx, cancelRun := d.requestContext(parent)
	defer cancelRun()
	cancelParent()
	select {
	case <-runCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("request-scoped CDP context did not cancel with parent")
	}
}
