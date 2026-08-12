package compressors

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
)

func TestCapabilityManifestStableAndComplete(t *testing.T) {
	registry := Default()
	first, err := registry.CapabilityManifest()
	if err != nil {
		t.Fatal(err)
	}
	second, err := registry.CapabilityManifest()
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatal("capability manifest is not deterministic")
	}
	var manifest CapabilityRegistry
	if err := json.Unmarshal(first, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.SchemaVersion != 1 || len(manifest.Capabilities) != 14 {
		t.Fatalf("unexpected manifest: version=%d capabilities=%d", manifest.SchemaVersion, len(manifest.Capabilities))
	}
	canonical, err := json.Marshal(manifest.Capabilities)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(canonical)
	if manifest.RegistrySHA256 != hex.EncodeToString(digest[:]) {
		t.Fatal("registry digest does not pin ordered capabilities")
	}
	for i, capability := range manifest.Capabilities {
		if i > 0 && manifest.Capabilities[i-1].TransformID >= capability.TransformID {
			t.Fatal("capabilities not sorted")
		}
		if capability.Provenance.Kind != "native" || capability.Provenance.SourceManifestSHA256 != nil {
			t.Fatalf("unexpected provenance for %s", capability.TransformID)
		}
		if capability.NotSmallerFallback != "original" || capability.Recovery != "exact_ccr" {
			t.Fatalf("unsafe fallback for %s", capability.TransformID)
		}
	}
}

// TestCapabilityManifestDigestIsPinned guards every Cave Build lock in the field.
// A lock records transform_registry_sha256 and the agent re-checks it at RUN
// time, so rotating this digest fails already-built agents with a stale-lock
// error they did nothing to cause. Registering a compressor that no compiled plan
// can route to must therefore leave the manifest alone — that is what
// manifestExcluded is for. Changing this constant is a deliberate ABI break that
// requires every lock to be rebuilt.
func TestCapabilityManifestDigestIsPinned(t *testing.T) {
	const pinned = "64b06ad2a9c99a5ff6a3ad7029ff9fa439c7435f5d94d01879c9e0e11e29513f"
	raw, err := Default().CapabilityManifest()
	if err != nil {
		t.Fatal(err)
	}
	var manifest CapabilityRegistry
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.RegistrySHA256 != pinned {
		t.Fatalf("registry digest rotated: got %s want %s — every built lock now fails at run time", manifest.RegistrySHA256, pinned)
	}
	for _, capability := range manifest.Capabilities {
		if capability.TransformID == "caveman.engine."+toolSchemaAnnotationsType+".v1" {
			t.Fatal("an excluded compressor reached the compiled-plan ABI")
		}
	}
	if _, ok := Default().For(toolSchemaAnnotationsType); !ok {
		t.Fatal("exclusion from the manifest must not unregister the compressor")
	}
}
