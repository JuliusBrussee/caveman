package mcp

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"testing"
)

func TestVerificationDoesNotConsumeRecovery(t *testing.T) {
	const original = "exact café bytes\n\t \n"
	eng := &storeEngine{originals: map[string]string{"ccr_fixture": original}}
	session := newRetrieveSession()
	args := json.RawMessage(`{"recovery_handle":"ccr://ccr_fixture","query":"ignored","verify_only":true}`)
	verify := func() {
		t.Helper()
		result := retrieveTool(eng, session, args)
		if result.IsError || len(result.Content) != 1 {
			t.Fatalf("verification failed: %+v", result)
		}
		var got struct {
			Handle string `json:"recovery_handle"`
			Bytes  int    `json:"byte_length"`
			SHA256 string `json:"sha256"`
		}
		if err := json.Unmarshal([]byte(result.Content[0].Text), &got); err != nil {
			t.Fatal(err)
		}
		if got.Handle != "ccr_fixture" || got.Bytes != len(original) || got.SHA256 != fmt.Sprintf("%x", sha256.Sum256([]byte(original))) {
			t.Fatalf("wrong recovery metadata: %+v", got)
		}
	}
	for range retrieveStormThreshold + 2 {
		verify()
	}
	full := retrieveTool(eng, session, retrieveArgs("ccr_fixture", ""))
	if full.IsError || len(full.Content) != 1 || full.Content[0].Text != original {
		t.Fatalf("verification consumed or decorated first recovery: %+v", full)
	}
	verify()
}

func TestVerificationRejectsUnknownHandle(t *testing.T) {
	result := retrieveTool(newStoreEngine(), newRetrieveSession(), json.RawMessage(`{"recovery_handle":"ccr_missing","verify_only":true}`))
	if !result.IsError {
		t.Fatalf("unknown handle verified: %+v", result)
	}
}
