package gateway

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
)

// Body capture is a local diagnostic instrument, off unless CAVE_CAPTURE_DIR is
// set. It writes the bytes a request carried on BOTH sides of the transform —
// what the client sent and what the upstream received — so the question "what
// was actually in that request" is answered by observation.
//
// Bodies are stored BASE64-encoded (`body_encoding: "base64"`), never as JSON
// strings. A request body is arbitrary bytes and the truncation-style
// compressors this instrument diagnoses cut mid-rune, so a JSON string would
// silently substitute U+FFFD for invalid UTF-8 and the file would fail its own
// recorded sha256. The recorded hash always covers the WHOLE body and always
// verifies against the decoded bytes.
//
// It exists because it was not answerable before. The 2026-08-06 agent bench
// measured a wrap arm whose prefix carried ~61k tokens the bare arm's did not,
// and no artifact in the repo could say what those bytes were: the CLI
// transcript records what the agent sent, provider usage records what the
// provider counted, and nothing recorded what went over the wire between them.
// Every diagnosis of that gap was inference.
//
// It is deliberately NOT wired to any product surface, telemetry sink, or
// savings figure. Captured bytes are request payloads — complete, UNREDACTED
// prompts, by design: redacted bytes cannot answer the question the instrument
// exists for. They belong on the operator's own disk, under a directory the
// operator named, and nowhere else. There is no TTL, no rotation, and no
// upload; deleting a capture directory is the operator's job.
//
// Nothing here may slow or fail traffic. record() hashes nothing, writes
// nothing, and never blocks: it hands the job to a single writer goroutine
// through a bounded queue and DROPS the record when the queue is full (drops are
// counted and logged, and the gap they leave in `seq` is itself visible in the
// file listing).
type bodyCapture struct {
	dir     string
	logger  *slog.Logger
	seq     atomic.Uint64
	jobs    chan captureJob
	queued  atomic.Int64
	dropped atomic.Uint64
}

const (
	// captureBodyLimit caps what one side of one request may put on disk. Over the
	// cap the body is omitted ENTIRELY rather than stored partially: a prefix that
	// fails the record's own sha256 is worse evidence than no bytes at all. The
	// hash and the length still describe the whole body.
	captureBodyLimit = 4 << 20
	// captureQueueDepth and captureQueueBytes bound what the instrument may hold in
	// memory while the writer catches up. Past either bound a record is dropped —
	// the request path never waits on a disk write.
	//
	// The byte budget is reserved on the FULL body size, not the stored
	// (capped) size, because the queue really does hold the full bytes — the
	// writer needs them to hash the whole body before applying captureBodyLimit.
	// A burst of over-cap bodies therefore transiently occupies budget for
	// records that store no bytes; that is the budget bounding memory, its job.
	captureQueueDepth = 32
	captureQueueBytes = 32 << 20
	// captureDropLogEvery keeps drop reporting visible without letting a slow disk
	// turn into a log flood.
	captureDropLogEvery = 64
)

// newBodyCapture returns nil when capture is off, so the hot path costs one nil
// check. A directory that cannot be created disables capture rather than
// failing the proxy: an instrument must never be able to break traffic.
func newBodyCapture(dir string, logger *slog.Logger) *bodyCapture {
	if dir == "" {
		return nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil
	}
	c := &bodyCapture{dir: dir, logger: logger, jobs: make(chan captureJob, captureQueueDepth)}
	go c.run()
	return c
}

// capturedRequest is one request's two bodies plus the identity needed to line
// it up against a provider response. Sizes and hashes are recorded next to the
// bodies so a capture stays interpretable even when a body is omitted.
type capturedRequest struct {
	Seq         uint64 `json:"seq"`
	RequestID   string `json:"request_id"`
	Provider    string `json:"provider"`
	Endpoint    string `json:"endpoint"`
	RuntimeMode string `json:"runtime_mode"`
	Optimizers  string `json:"optimizers"`
	// RetryOriginal marks the SECOND record written for a request id after the
	// upstream rejected the transformed bytes and the proxy resent the original
	// (the 4xx fail-open in proxy.go and chatgpt.go). Its presence means the
	// earlier record for that id did not serve the request — these bytes did.
	RetryOriginal bool `json:"retry_original,omitempty"`
	// BodyEncoding names how ClientBody/UpstreamBody are encoded in this file, so a
	// reader never has to guess. Always "base64".
	BodyEncoding   string `json:"body_encoding"`
	ClientBytes    int    `json:"client_bytes"`
	UpstreamBytes  int    `json:"upstream_bytes"`
	ClientSHA256   string `json:"client_sha256"`
	UpstreamSHA256 string `json:"upstream_sha256"`
	Transformed    bool   `json:"transformed"`
	ClientBody     []byte `json:"client_body,omitempty"`
	UpstreamBody   []byte `json:"upstream_body,omitempty"`
	// UpstreamOmitted flags the pass-through case: identical bodies are stored
	// once. ClientOmitted/UpstreamOmitted*Reason* name the other two reasons bytes
	// are absent — "oversize" (over captureBodyLimit) and "streamed" (the caller
	// forwarded the body without ever holding it whole). In every case the hash and
	// the length still describe the whole body.
	UpstreamOmitted       bool   `json:"upstream_body_omitted_identical"`
	ClientOmittedReason   string `json:"client_body_omitted,omitempty"`
	UpstreamOmittedReason string `json:"upstream_body_omitted,omitempty"`
}

// captureMeta is the identity of one captured attempt. It carries no bytes so
// the byte slices stay explicit at the call site.
type captureMeta struct {
	RequestID     string
	Provider      string
	Endpoint      string
	RuntimeMode   string
	Optimizers    string
	RetryOriginal bool
}

// captureBody is one side's body as the CALLER holds it. wholeBody is the normal
// case; streamedBody is for a caller that forwarded the body straight through and
// only ever held a bounded prefix of it (the ChatGPT streaming path), which can
// still state the whole body's length and hash truthfully but has no bytes to
// store.
type captureBody struct {
	bytes []byte
	total int
	sum   string
}

func wholeBody(b []byte) captureBody { return captureBody{bytes: b, total: len(b)} }

func streamedBody(total int, sumHex string) captureBody {
	return captureBody{total: total, sum: sumHex}
}

type captureJob struct {
	seq      uint64
	meta     captureMeta
	client   captureBody
	upstream captureBody
	size     int64
	// barrier is a test-only ordering marker: the writer closes it in queue order,
	// which is how a test knows every earlier record has landed on disk.
	barrier chan struct{}
}

// record queues one capture. Every failure path is silent-and-continue by
// design: this runs on the request path, and a diagnostic that can fail or slow
// a user's request is worse than no diagnostic at all. Hashing, encoding, and
// the disk write all happen on the writer goroutine.
func (c *bodyCapture) record(meta captureMeta, client, upstream captureBody) {
	if c == nil {
		return
	}
	size := int64(len(client.bytes) + len(upstream.bytes))
	if c.queued.Add(size) > captureQueueBytes {
		c.queued.Add(-size)
		c.drop()
		return
	}
	job := captureJob{seq: c.seq.Add(1), meta: meta, client: client, upstream: upstream, size: size}
	select {
	case c.jobs <- job:
	default:
		c.queued.Add(-size)
		c.drop()
	}
}

func (c *bodyCapture) drop() {
	n := c.dropped.Add(1)
	if c.logger != nil && (n == 1 || n%captureDropLogEvery == 0) {
		c.logger.Warn("body capture dropped records; capture is lossy under load", "dropped_total", n, "dir", c.dir)
	}
}

// flush blocks until every record queued before the call has been written. It
// exists so tests can assert against the files; the request path never calls it.
func (c *bodyCapture) flush() {
	if c == nil {
		return
	}
	barrier := make(chan struct{})
	c.jobs <- captureJob{barrier: barrier}
	<-barrier
}

func (c *bodyCapture) run() {
	for job := range c.jobs {
		if job.barrier != nil {
			close(job.barrier)
			continue
		}
		c.write(job)
		c.queued.Add(-job.size)
	}
}

func (c *bodyCapture) write(job captureJob) {
	client := job.client.resolve()
	upstream := job.upstream.resolve()
	// An empty hash means the caller could not state one; it must never read as a
	// match, because "identical" is a claim.
	identical := client.sum != "" && client.sum == upstream.sum

	entry := capturedRequest{
		Seq:                 job.seq,
		RequestID:           job.meta.RequestID,
		Provider:            job.meta.Provider,
		Endpoint:            job.meta.Endpoint,
		RuntimeMode:         job.meta.RuntimeMode,
		Optimizers:          job.meta.Optimizers,
		RetryOriginal:       job.meta.RetryOriginal,
		BodyEncoding:        "base64",
		ClientBytes:         client.total,
		UpstreamBytes:       upstream.total,
		ClientSHA256:        client.sum,
		UpstreamSHA256:      upstream.sum,
		Transformed:         !identical,
		ClientBody:          client.bytes,
		ClientOmittedReason: client.omitted,
		UpstreamOmitted:     identical,
	}
	// An untransformed request would otherwise store the same payload twice, and
	// these files are large. The hashes still prove the two sides matched.
	if !identical {
		entry.UpstreamBody = upstream.bytes
		entry.UpstreamOmittedReason = upstream.omitted
	}

	encoded, err := json.Marshal(entry)
	if err != nil {
		return
	}
	name := strconv.FormatUint(job.seq, 10) + "-" + sanitizeCaptureID(job.meta.RequestID) + ".json"
	_ = os.WriteFile(filepath.Join(c.dir, name), encoded, 0o600)
}

// resolvedBody is one side after the writer has hashed it and applied the size
// cap: bytes are present or they are not, and the hash and length always cover
// the whole body either way.
type resolvedBody struct {
	bytes   []byte
	total   int
	sum     string
	omitted string
}

func (b captureBody) resolve() resolvedBody {
	out := resolvedBody{total: b.total, sum: b.sum}
	held := b.bytes != nil || b.total == 0
	if out.sum == "" && held {
		sum := sha256.Sum256(b.bytes)
		out.sum = hex.EncodeToString(sum[:])
	}
	switch {
	case !held:
		out.omitted = "streamed"
	case len(b.bytes) > captureBodyLimit:
		out.omitted = "oversize"
	default:
		out.bytes = b.bytes
	}
	return out
}

// sanitizeCaptureID keeps a request id usable as a filename. An id is
// proxy-generated, but it reaches this function as a string and a path
// separator in a filename is a directory traversal, so it is filtered rather
// than trusted.
func sanitizeCaptureID(id string) string {
	if id == "" {
		return "unknown"
	}
	out := make([]byte, 0, len(id))
	for i := 0; i < len(id) && i < 64; i++ {
		ch := id[i]
		switch {
		case ch >= 'a' && ch <= 'z', ch >= 'A' && ch <= 'Z', ch >= '0' && ch <= '9', ch == '-', ch == '_':
			out = append(out, ch)
		default:
			out = append(out, '_')
		}
	}
	return string(out)
}
