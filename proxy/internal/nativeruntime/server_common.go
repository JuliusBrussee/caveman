package nativeruntime

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"time"
)

// Call sends one bounded host event to the shared runtime. Client adapters use
// same platform endpoint and 250 ms deadline as server; any transport or schema
// failure returns no decision so host remains fail-open.
func Call(ctx context.Context, home string, request Request) (*Response, error) {
	callCtx, cancel := context.WithTimeout(ctx, hookDeadline)
	defer cancel()
	conn, err := dialNativeRuntime(callCtx, home)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(hookDeadline))
	if err := json.NewEncoder(conn).Encode(request); err != nil {
		return nil, err
	}
	limited := &io.LimitedReader{R: conn, N: maxRequestBytes + 1}
	var response Response
	if err := json.NewDecoder(limited).Decode(&response); err != nil {
		return nil, err
	}
	if limited.N <= 0 || !validResponse(response) {
		return nil, errors.New("native runtime: invalid response")
	}
	return &response, nil
}

func validResponse(response Response) bool {
	if response.ProtocolVersion != ProtocolVersion || !response.FailOpen {
		return false
	}
	if response.PolicyMode != "" && response.PolicyMode != "record" && response.PolicyMode != "safe" && response.PolicyMode != "max" {
		return false
	}
	if response.Profile != "" {
		switch response.Profile {
		case "record-only", "core", "core-lean-build", "ledger", "ccr-masking", "cache-aware", "full-safe", "full-max":
		default:
			return false
		}
	}
	switch response.Action {
	case "allow", "observe", "block", "ask", "defer":
	default:
		return false
	}
	switch response.Visibility {
	case "silent", "advisory", "user":
	default:
		return false
	}
	if len(response.Context) > 64*1024 || len(response.Message) > 4096 || len(response.OutputReplacement) > 2*1024*1024 || len(response.RecoveryRef) > 1024 || len(response.DecisionID) > 256 {
		return false
	}
	return true
}

const (
	maxRequestBytes = 2 * 1024 * 1024
	hookDeadline    = 250 * time.Millisecond
)

func serveConn(ctx context.Context, conn net.Conn, runtime *Runtime) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(hookDeadline))
	hookCtx, cancel := context.WithTimeout(ctx, hookDeadline)
	defer cancel()
	limited := &io.LimitedReader{R: conn, N: maxRequestBytes + 1}
	var request Request
	if err := json.NewDecoder(limited).Decode(&request); err != nil || limited.N <= 0 {
		return
	}
	response, err := runtime.Handle(hookCtx, request)
	if err != nil {
		// Closing without a decision is fail-open at every thin host adapter.
		// Never synthesize success: callers use a real decision_id to prove the
		// session reached shared runtime before emitting a correlation marker.
		return
	}
	_ = json.NewEncoder(conn).Encode(response)
}
