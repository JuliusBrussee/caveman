package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/JuliusBrussee/caveman/engine/ccr"
	"github.com/JuliusBrussee/caveman/proxy/internal/gateway"
	"github.com/JuliusBrussee/caveman/proxy/internal/nativeruntime"
	"github.com/JuliusBrussee/caveman/proxy/internal/store"
)

type receiptTestAuth struct{}

func (receiptTestAuth) Authenticate(context.Context, *http.Request) (gateway.RequestContext, error) {
	return gateway.RequestContext{RuntimeMode: "record"}, nil
}

func TestChatGPTUsageReachesNativeReceipt(t *testing.T) {
	t.Parallel()
	for _, model := range []string{"gpt-6.1-sol", gateway.AutoModel} {
		t.Run(model, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"model":"gpt-6.1-sol","usage":{"input_tokens":10,"output_tokens":2,"total_tokens":12}}`)
			}))
			defer upstream.Close()
			spend, err := store.Open(":memory:", nil)
			if err != nil {
				t.Fatal(err)
			}
			defer spend.Close()
			objects, err := ccr.OpenMemory()
			if err != nil {
				t.Fatal(err)
			}
			defer objects.Close()
			key := bytes.Repeat([]byte{29}, 32)
			server := gateway.New(gateway.Config{
				Auth: receiptTestAuth{}, Sink: spend, AsyncRecord: true,
				ChatGPTUpstream: upstream.URL, HTTPClient: &http.Client{}, SessionMarkerKey: key,
			})
			defer server.Close()
			receipts := t.TempDir()
			runtime := nativeruntime.NewWithReceiptsAndUsage(objects, receipts, usageAfterFlush{flush: server.Flush, spend: spend})
			for _, host := range []string{"receipt-a", "receipt-b"} {
				session := nativeruntime.Session{ID: "codex:" + host, HostSessionID: host}
				if _, err := runtime.Handle(context.Background(), nativeruntime.Request{
					ProtocolVersion: 1, Agent: nativeruntime.Agent{ID: "codex"},
					Session: session, Event: nativeruntime.Event{Type: "session.start"},
				}); err != nil {
					t.Fatal(err)
				}
				marker, err := nativeruntime.SessionMarker(key, session.ID)
				if err != nil {
					t.Fatal(err)
				}
				body, err := json.Marshal(map[string]any{"model": model, "instructions": marker, "input": "Explain E_TIMEOUT."})
				if err != nil {
					t.Fatal(err)
				}
				request := httptest.NewRequest(http.MethodPost, "/chatgpt/responses", bytes.NewReader(body))
				request.Header.Set("Authorization", "Bearer test-only")
				response := httptest.NewRecorder()
				server.Handler().ServeHTTP(response, request)
				if response.Code != http.StatusOK {
					t.Fatalf("provider response=%d", response.Code)
				}
			}
			for _, host := range []string{"receipt-a", "receipt-b"} {
				if _, err := runtime.Handle(context.Background(), nativeruntime.Request{
					ProtocolVersion: 1, Agent: nativeruntime.Agent{ID: "codex"},
					Session: nativeruntime.Session{ID: "codex:" + host, HostSessionID: host},
					Event:   nativeruntime.Event{Type: "session.end"},
				}); err != nil {
					t.Fatal(err)
				}
				data, err := os.ReadFile(filepath.Join(receipts, "latest.json"))
				if err != nil {
					t.Fatal(err)
				}
				var receipt nativeruntime.Receipt
				if err := json.Unmarshal(data, &receipt); err != nil {
					t.Fatal(err)
				}
				usage := receipt.ProviderUsage
				if receipt.HostSession != host || usage.Status != "correlated" || usage.CorrelationBasis != "signed_marker" || usage.Requests != 1 || usage.InputTokens != 10 || usage.OutputTokens != 2 {
					t.Fatalf("session %s received incorrect receipt: %+v", host, usage)
				}
				if usage.CatalogListPriceSubtotalUSD != 0 || usage.InferredSavingsUSD != 0 || receipt.Claims["task_savings"] != "not_verified_without_paired_baseline" {
					t.Fatal("subscription receipt invented prices or verified savings")
				}
			}
		})
	}
}
