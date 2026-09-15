package objectstore

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestCompressRoundTrip(t *testing.T) {
	payload := []byte(strings.Repeat("the quick brown fox jumps over the lazy dog. ", 200))
	for _, codec := range []string{CompressionNone, CompressionGzip, CompressionZstd} {
		t.Run(codec, func(t *testing.T) {
			enc, err := Compress(codec, payload)
			if err != nil {
				t.Fatalf("compress: %v", err)
			}
			if codec != CompressionNone && len(enc) >= len(payload) {
				t.Fatalf("%s did not shrink repetitive payload: %d >= %d", codec, len(enc), len(payload))
			}
			dec, err := Decompress(codec, enc)
			if err != nil {
				t.Fatalf("decompress: %v", err)
			}
			if !bytes.Equal(dec, payload) {
				t.Fatalf("%s round-trip mismatch", codec)
			}
		})
	}
}

func TestCompressUnknownCodecFailsClosed(t *testing.T) {
	if _, err := Compress("brotli", []byte("x")); err == nil {
		t.Fatal("expected unknown codec to error")
	}
	if _, err := Decompress("brotli", []byte("x")); err == nil {
		t.Fatal("expected unknown codec to error")
	}
}

func TestDecompressLimitRejectsExpansionBeforeReturningPlaintext(t *testing.T) {
	payload := []byte(strings.Repeat("highly compressible payload ", 4096))
	for _, codec := range []string{CompressionNone, CompressionGzip, CompressionZstd} {
		t.Run(codec, func(t *testing.T) {
			encoded, err := Compress(codec, payload)
			if err != nil {
				t.Fatalf("compress: %v", err)
			}
			got, err := DecompressLimit(codec, encoded, 1024)
			if !errors.Is(err, ErrDecompressedTooLarge) {
				t.Fatalf("DecompressLimit error = %v, want ErrDecompressedTooLarge", err)
			}
			if got != nil {
				t.Fatalf("DecompressLimit returned %d plaintext bytes on rejection", len(got))
			}
		})
	}
}

func TestDecompressLimitAllowsExactBoundary(t *testing.T) {
	payload := []byte(strings.Repeat("x", 4096))
	for _, codec := range []string{CompressionNone, CompressionGzip, CompressionZstd} {
		t.Run(codec, func(t *testing.T) {
			encoded, err := Compress(codec, payload)
			if err != nil {
				t.Fatalf("compress: %v", err)
			}
			got, err := DecompressLimit(codec, encoded, int64(len(payload)))
			if err != nil {
				t.Fatalf("decompress exact boundary: %v", err)
			}
			if !bytes.Equal(got, payload) {
				t.Fatal("exact-boundary round trip mismatch")
			}
		})
	}
}

func TestDecompressLimitRejectsInvalidCeiling(t *testing.T) {
	for _, limit := range []int64{-1, DefaultMaxDecompressedBytes + 1} {
		if _, err := DecompressLimit(CompressionNone, nil, limit); err == nil {
			t.Fatalf("limit %d accepted", limit)
		}
	}
}
