package objectstore

import (
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"

	"github.com/klauspost/compress/zstd"
)

// Compression names the supported codecs; they mirror the artifacts.compression
// CHECK constraint (none|gzip|zstd). An unknown codec fails closed.
const (
	CompressionNone = "none"
	CompressionGzip = "gzip"
	CompressionZstd = "zstd"
)

var (
	zstdEnc, _ = zstd.NewWriter(nil)

	// ErrDecompressedTooLarge is returned before a compressed payload can
	// expand beyond the caller's plaintext ceiling.
	ErrDecompressedTooLarge = errors.New("objectstore: decompressed payload exceeds limit")
)

// DefaultMaxDecompressedBytes protects callers that do not have trusted size
// metadata. Callers with an expected plaintext size should use
// DecompressLimit with the smaller, metadata-validated limit.
const DefaultMaxDecompressedBytes int64 = 128 << 20

// Compress encodes data with the named codec. "none" returns data unchanged.
func Compress(codec string, data []byte) ([]byte, error) {
	switch codec {
	case CompressionNone, "":
		return data, nil
	case CompressionGzip:
		var buf bytes.Buffer
		zw := gzip.NewWriter(&buf)
		if _, err := zw.Write(data); err != nil {
			return nil, fmt.Errorf("gzip: %w", err)
		}
		if err := zw.Close(); err != nil {
			return nil, fmt.Errorf("gzip close: %w", err)
		}
		return buf.Bytes(), nil
	case CompressionZstd:
		return zstdEnc.EncodeAll(data, nil), nil
	default:
		return nil, fmt.Errorf("unknown compression codec %q", codec)
	}
}

// Decompress reverses Compress with a defensive default expansion ceiling. An
// unknown codec fails closed.
func Decompress(codec string, data []byte) ([]byte, error) {
	return DecompressLimit(codec, data, DefaultMaxDecompressedBytes)
}

// DecompressLimit reverses Compress while refusing to produce more than
// maxBytes of plaintext. The limit is applied while decoding gzip and through
// zstd's decoder memory bound, so highly-compressible hostile payloads cannot
// first allocate their full expanded size and only then be rejected.
func DecompressLimit(codec string, data []byte, maxBytes int64) ([]byte, error) {
	if maxBytes < 0 || maxBytes > DefaultMaxDecompressedBytes {
		return nil, fmt.Errorf("invalid decompression limit %d", maxBytes)
	}
	switch codec {
	case CompressionNone, "":
		if int64(len(data)) > maxBytes {
			return nil, ErrDecompressedTooLarge
		}
		return data, nil
	case CompressionGzip:
		zr, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("gzip reader: %w", err)
		}
		defer zr.Close()
		out, err := io.ReadAll(io.LimitReader(zr, maxBytes+1))
		if err != nil {
			return nil, fmt.Errorf("gzip read: %w", err)
		}
		if int64(len(out)) > maxBytes {
			return nil, ErrDecompressedTooLarge
		}
		return out, nil
	case CompressionZstd:
		// Zstd's smallest legal window is 1 KiB. Keep that parser minimum for
		// tiny valid payloads, then independently enforce the exact byte limit.
		memoryLimit := uint64(maxBytes)
		if memoryLimit < zstd.MinWindowSize {
			memoryLimit = zstd.MinWindowSize
		}
		zr, err := zstd.NewReader(nil,
			zstd.WithDecoderConcurrency(1),
			zstd.WithDecoderMaxMemory(memoryLimit),
		)
		if err != nil {
			return nil, fmt.Errorf("zstd reader: %w", err)
		}
		defer zr.Close()
		out, err := zr.DecodeAll(data, nil)
		if err != nil {
			if errors.Is(err, zstd.ErrDecoderSizeExceeded) || errors.Is(err, zstd.ErrWindowSizeExceeded) {
				return nil, ErrDecompressedTooLarge
			}
			return nil, fmt.Errorf("zstd read: %w", err)
		}
		if int64(len(out)) > maxBytes {
			return nil, ErrDecompressedTooLarge
		}
		return out, nil
	default:
		return nil, fmt.Errorf("unknown compression codec %q", codec)
	}
}
