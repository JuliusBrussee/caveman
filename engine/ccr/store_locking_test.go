//go:build !js

package ccr

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

type lockingRecovery struct {
	Handle string
	Object string
	Data   []byte
}

type lockingConsumerRequest struct {
	Path       string
	Recoveries []lockingRecovery
}

// The writer must stay open while independent consumers come and go. Closing
// the writer before checking would checkpoint its WAL and hide lost locks.
func TestOpenClosePeerPreservesLiveWriter(t *testing.T) {
	t.Setenv("CAVEMAN_CCR_MAX_BYTES", "")
	for _, scenario := range []string{"post-open", "prepare-live-store", "in-process-opens"} {
		t.Run(scenario, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "ccr.db")
			writer, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer writer.Close()
			request := lockingConsumerRequest{Path: path}
			for round := range 3 {
				data := []byte(fmt.Sprintf("%s round %d: exact bytes\x00\xff\r\n", scenario, round))
				handle, err := writer.Put(Recovery{ContentType: "text", Compressor: "text", Original: data})
				if err != nil {
					t.Fatalf("write after consumer closed: %v", err)
				}
				object, err := writer.PutObject(Object{Type: ObjectCommandResult, SessionID: "lock-regression", Data: data})
				if err != nil {
					t.Fatalf("write object after consumer closed: %v", err)
				}
				request.Recoveries = append(request.Recoveries, lockingRecovery{Handle: handle, Object: object, Data: data})
				switch scenario {
				case "prepare-live-store":
					if err := PrepareSQLitePath(path); err != nil {
						t.Fatal(err)
					}
				case "in-process-opens":
					peer, err := Open(path)
					if err != nil {
						t.Fatal(err)
					}
					if err := peer.Close(); err != nil {
						t.Fatal(err)
					}
				}
				runLockingConsumer(t, request)
			}
		})
	}
}

func runLockingConsumer(t *testing.T, request lockingConsumerRequest) {
	t.Helper()
	input, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "-test.run=^TestSQLiteLockingConsumerProcess$")
	cmd.Env = append(os.Environ(), "CAVEMAN_CCR_LOCKING_CONSUMER=1")
	cmd.Stdin = bytes.NewReader(input)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fresh consumer: %v\n%s", err, output)
	}
}

func TestSQLiteLockingConsumerProcess(t *testing.T) {
	if os.Getenv("CAVEMAN_CCR_LOCKING_CONSUMER") != "1" {
		return
	}
	var request lockingConsumerRequest
	if err := json.NewDecoder(os.Stdin).Decode(&request); err != nil {
		t.Fatal(err)
	}
	consumer, err := Open(request.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer consumer.Close()
	for _, recovery := range request.Recoveries {
		got, err := consumer.Get(recovery.Handle)
		if err != nil || !bytes.Equal(got, recovery.Data) {
			t.Fatalf("recovery %s: bytes=%q, error=%v; want %q", recovery.Handle, got, err, recovery.Data)
		}
		object, err := consumer.GetObject(recovery.Object)
		if err != nil || !bytes.Equal(object.Data, recovery.Data) {
			t.Fatalf("object %s: bytes=%q, error=%v; want %q", recovery.Object, object.Data, err, recovery.Data)
		}
	}
	if err := consumer.Close(); err != nil {
		t.Fatal(err)
	}
}
