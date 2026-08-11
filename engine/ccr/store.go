// Package ccr is the Caveman Context Recovery store: it keeps the exact original
// bytes of every lossy (S4) compression, keyed by a handle, so retrieve(handle)
// returns the original byte-for-byte and nothing the engine compresses is ever
// destroyed.
//
// The handle is content-addressed (sha256 of the original), which makes the
// engine idempotent — compressing the same payload twice yields the same handle
// and stores it once.
//
// The Store implementation is platform-split: a local SQLite database on host
// platforms (store_sqlite.go) and a pure-Go in-memory map under js/wasm
// (store_wasm.go), since modernc.org/sqlite does not build for js/wasm. Both
// expose the same type and methods, so the engine is unaware of the difference.
package ccr

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// ErrNotFound is returned by Get when a handle is unknown. The store never
// guesses a recovery — an unknown handle is an explicit miss.
var ErrNotFound = errors.New("ccr: recovery handle not found")

// ErrBudgetExceeded means a new recovery was refused before publishing lossy
// bytes because the local store's configured payload budget would be exceeded.
// Existing handles remain intact and retrievable; callers must pass through.
var ErrBudgetExceeded = errors.New("ccr: storage budget exceeded")

// ObjectType is a closed typed-working-memory enum. Unknown values fail closed:
// adapters may preserve unknown native payloads outside CCR, but may not invent
// retrieval semantics for them.
type ObjectType string

const (
	ObjectFileObservation      ObjectType = "FileObservation"
	ObjectSearchResult         ObjectType = "SearchResult"
	ObjectCommandResult        ObjectType = "CommandResult"
	ObjectTestResult           ObjectType = "TestResult"
	ObjectBuildResult          ObjectType = "BuildResult"
	ObjectDiffSnapshot         ObjectType = "DiffSnapshot"
	ObjectTaskContract         ObjectType = "TaskContract"
	ObjectTaskDecision         ObjectType = "TaskDecision"
	ObjectExecutionState       ObjectType = "ExecutionState"
	ObjectDocumentationExcerpt ObjectType = "DocumentationExcerpt"
	ObjectBrowserSnapshot      ObjectType = "BrowserSnapshot"
	ObjectRepositoryMap        ObjectType = "RepositoryMap"
	ObjectEvidenceBundle       ObjectType = "EvidenceBundle"
)

type Currentness string

const (
	Current  Currentness = "current"
	Stale    Currentness = "stale"
	Archived Currentness = "archived"
)

type Lifecycle string

const (
	Hot               Lifecycle = "hot"
	Warm              Lifecycle = "warm"
	Cold              Lifecycle = "cold"
	LifecycleArchived Lifecycle = "archived"
)

// Object is one exact typed working-memory record. Data remains byte-exact;
// masking lives above this store and must retain ObjectID as its recovery path.
type Object struct {
	ID                 string      `json:"object_id"`
	Type               ObjectType  `json:"type"`
	ContentHash        string      `json:"content_hash"`
	Source             string      `json:"source"`
	CreatedAt          time.Time   `json:"created_at"`
	RepositoryState    string      `json:"repository_state"`
	SessionID          string      `json:"session_id"`
	TransformVersion   string      `json:"transform_version"`
	Currentness        Currentness `json:"currentness"`
	Lifecycle          Lifecycle   `json:"lifecycle"`
	Dependencies       []string    `json:"dependencies"`
	OriginalByteLength int         `json:"original_byte_length"`
	StoredByteLength   int         `json:"stored_byte_length"`
	Data               []byte      `json:"data"`
}

var objectTypes = map[ObjectType]struct{}{
	ObjectFileObservation: {}, ObjectSearchResult: {}, ObjectCommandResult: {},
	ObjectTestResult: {}, ObjectBuildResult: {}, ObjectDiffSnapshot: {},
	ObjectTaskContract: {}, ObjectTaskDecision: {}, ObjectExecutionState: {},
	ObjectDocumentationExcerpt: {}, ObjectBrowserSnapshot: {},
	ObjectRepositoryMap: {}, ObjectEvidenceBundle: {},
}

func prepareObject(obj Object) (Object, error) {
	if _, ok := objectTypes[obj.Type]; !ok {
		return Object{}, fmt.Errorf("ccr: unknown object type %q", obj.Type)
	}
	if strings.TrimSpace(obj.SessionID) == "" {
		return Object{}, errors.New("ccr: typed object session_id is required")
	}
	if obj.Currentness == "" {
		obj.Currentness = Current
	}
	if obj.Currentness != Current && obj.Currentness != Stale && obj.Currentness != Archived {
		return Object{}, fmt.Errorf("ccr: unknown currentness %q", obj.Currentness)
	}
	if obj.Lifecycle == "" {
		obj.Lifecycle = Hot
	}
	if obj.Lifecycle != Hot && obj.Lifecycle != Warm && obj.Lifecycle != Cold && obj.Lifecycle != LifecycleArchived {
		return Object{}, fmt.Errorf("ccr: unknown lifecycle %q", obj.Lifecycle)
	}
	if obj.CreatedAt.IsZero() {
		obj.CreatedAt = time.Now().UTC()
	} else {
		obj.CreatedAt = obj.CreatedAt.UTC()
	}
	dataSum := sha256.Sum256(obj.Data)
	computedHash := "sha256:" + hex.EncodeToString(dataSum[:])
	if obj.ContentHash == "" {
		obj.ContentHash = computedHash
	} else if obj.ContentHash != computedHash {
		return Object{}, errors.New("ccr: typed object content_hash does not match data")
	}
	if obj.OriginalByteLength == 0 {
		obj.OriginalByteLength = len(obj.Data)
	}
	if obj.StoredByteLength == 0 {
		obj.StoredByteLength = len(obj.Data)
	}
	if obj.OriginalByteLength < 0 || obj.StoredByteLength < 0 {
		return Object{}, errors.New("ccr: typed object byte lengths cannot be negative")
	}
	if obj.ID == "" {
		identity := strings.Join([]string{string(obj.Type), obj.SessionID, obj.Source, obj.RepositoryState, obj.ContentHash}, "\x00")
		sum := sha256.Sum256([]byte(identity))
		obj.ID = "ccr_obj_" + hex.EncodeToString(sum[:16])
	}
	obj.Dependencies = append([]string(nil), obj.Dependencies...)
	obj.Data = bytes.Clone(obj.Data)
	return obj, nil
}

// sameImmutableObject compares fields PutObject promises never to change.
// CreatedAt is assigned at first storage, while currentness and lifecycle have
// explicit mutation methods, so repeat puts intentionally do not compare them.
func sameImmutableObject(left, right Object) bool {
	return left.ID == right.ID &&
		left.Type == right.Type &&
		left.ContentHash == right.ContentHash &&
		left.Source == right.Source &&
		left.RepositoryState == right.RepositoryState &&
		left.SessionID == right.SessionID &&
		left.TransformVersion == right.TransformVersion &&
		slices.Equal(left.Dependencies, right.Dependencies) &&
		left.OriginalByteLength == right.OriginalByteLength &&
		left.StoredByteLength == right.StoredByteLength &&
		bytes.Equal(left.Data, right.Data)
}

func validateCurrentness(value Currentness) error {
	if value != Current && value != Stale && value != Archived {
		return fmt.Errorf("ccr: unknown currentness %q", value)
	}
	return nil
}

func validateLifecycle(value Lifecycle) error {
	if value != Hot && value != Warm && value != Cold && value != LifecycleArchived {
		return fmt.Errorf("ccr: unknown lifecycle %q", value)
	}
	return nil
}

// Recovery is one stored original payload plus the accounting needed for stats.
type Recovery struct {
	ContentType  string
	Compressor   string
	TokensBefore int
	TokensAfter  int
	Original     []byte
	Metadata     []byte
}

// Bucket is aggregate compression accounting for one content type (or the total).
type Bucket struct {
	Count        int     `json:"count"`
	TokensBefore int     `json:"tokens_before"`
	TokensAfter  int     `json:"tokens_after"`
	Ratio        float64 `json:"ratio"`
}

// Stats is the engine's aggregate view, drawn from stored recoveries.
type Stats struct {
	Totals        Bucket            `json:"totals"`
	ByContentType map[string]Bucket `json:"by_content_type"`
	// Basis identifies the local token-count basis used for these ratios.
	Basis string `json:"basis"`
	// StorageBytes is exact retained payload+metadata bytes, not SQLite file
	// overhead. MaxStorageBytes is the configured hard logical budget.
	StorageBytes    int64 `json:"storage_bytes,omitempty"`
	MaxStorageBytes int64 `json:"max_storage_bytes,omitempty"`
	StorageFull     bool  `json:"storage_full,omitempty"`
}

// Handle returns the content-addressed handle for a payload without storing it.
func Handle(original []byte) string {
	sum := sha256.Sum256(original)
	return "ccr_" + hex.EncodeToString(sum[:16])
}

func ratio(before, after int) float64 {
	if before <= 0 {
		return 0
	}
	return float64(before-after) / float64(before)
}
