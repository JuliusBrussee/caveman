// Ported from pxpipe (https://github.com/teamchong/pxpipe), MIT License, Copyright (c) 2026 claude-image-proxy contributors.

package pixel

import (
	"bytes"
	"compress/gzip"
	_ "embed"
	"encoding/binary"
	"fmt"
	"io"
	"sort"
	"sync"
)

const (
	AtlasCellW     = 5
	AtlasCellH     = 8
	AtlasGrayCellW = 5
	AtlasGrayCellH = 8
)

//go:embed assets/atlas-codepoints.bin.gz
var atlasCodepointsGZ []byte

//go:embed assets/atlas-offsets.bin.gz
var atlasOffsetsGZ []byte

//go:embed assets/atlas-wideflags.bin.gz
var atlasWideFlagsGZ []byte

//go:embed assets/atlas-pixels.bin.gz
var atlasPixelsGZ []byte

//go:embed assets/atlasgray-codepoints.bin.gz
var atlasGrayCodepointsGZ []byte

//go:embed assets/atlasgray-offsets.bin.gz
var atlasGrayOffsetsGZ []byte

//go:embed assets/atlasgray-wideflags.bin.gz
var atlasGrayWideFlagsGZ []byte

//go:embed assets/atlasgray-pixels.bin.gz
var atlasGrayPixelsGZ []byte

var (
	atlasOnce sync.Once

	atlasCodepoints []uint32
	atlasOffsets    []uint32
	atlasWideFlags  []uint8
	atlasPixels     []uint8

	atlasGrayCodepoints []uint32
	atlasGrayOffsets    []uint32
	atlasGrayWideFlags  []uint8
	atlasGrayPixels     []uint8
)

func loadAtlas() {
	atlasOnce.Do(func() {
		atlasCodepoints = decodeU32Gzip("atlas-codepoints", atlasCodepointsGZ)
		atlasOffsets = decodeU32Gzip("atlas-offsets", atlasOffsetsGZ)
		atlasWideFlags = decodeBytesGzip("atlas-wideflags", atlasWideFlagsGZ)
		atlasPixels = decodeBytesGzip("atlas-pixels", atlasPixelsGZ)

		atlasGrayCodepoints = decodeU32Gzip("atlasgray-codepoints", atlasGrayCodepointsGZ)
		atlasGrayOffsets = decodeU32Gzip("atlasgray-offsets", atlasGrayOffsetsGZ)
		atlasGrayWideFlags = decodeBytesGzip("atlasgray-wideflags", atlasGrayWideFlagsGZ)
		atlasGrayPixels = decodeBytesGzip("atlasgray-pixels", atlasGrayPixelsGZ)

		checkAtlasLengths("atlas", atlasCodepoints, atlasOffsets, atlasWideFlags)
		checkAtlasLengths("atlasgray", atlasGrayCodepoints, atlasGrayOffsets, atlasGrayWideFlags)
	})
}

func checkAtlasLengths(name string, cps, offsets []uint32, wide []uint8) {
	if len(cps) != len(offsets) || len(cps) != len(wide) {
		panic(fmt.Sprintf("%s length mismatch: codepoints=%d offsets=%d wide=%d", name, len(cps), len(offsets), len(wide)))
	}
}

func decodeBytesGzip(name string, src []byte) []uint8 {
	zr, err := gzip.NewReader(bytes.NewReader(src))
	if err != nil {
		panic(fmt.Sprintf("%s gzip open: %v", name, err))
	}
	defer zr.Close()
	out, err := io.ReadAll(zr)
	if err != nil {
		panic(fmt.Sprintf("%s gzip read: %v", name, err))
	}
	return out
}

func decodeU32Gzip(name string, src []byte) []uint32 {
	raw := decodeBytesGzip(name, src)
	if len(raw)%4 != 0 {
		panic(fmt.Sprintf("%s byte length %d is not divisible by 4", name, len(raw)))
	}
	out := make([]uint32, len(raw)/4)
	for i := range out {
		out[i] = binary.LittleEndian.Uint32(raw[i*4 : i*4+4])
	}
	return out
}

// AtlasRank binary-searches the sorted codepoint table; -1 when absent.
func AtlasRank(cp rune) int {
	loadAtlas()
	target := uint32(cp)
	i := sort.Search(len(atlasCodepoints), func(i int) bool {
		return atlasCodepoints[i] >= target
	})
	if i < len(atlasCodepoints) && atlasCodepoints[i] == target {
		return i
	}
	return -1
}

// AtlasGrayRank binary-searches the sorted grayscale codepoint table; -1 when absent.
func AtlasGrayRank(cp rune) int {
	loadAtlas()
	target := uint32(cp)
	i := sort.Search(len(atlasGrayCodepoints), func(i int) bool {
		return atlasGrayCodepoints[i] >= target
	})
	if i < len(atlasGrayCodepoints) && atlasGrayCodepoints[i] == target {
		return i
	}
	return -1
}

func atlasBit(rank, row, col int) uint8 {
	loadAtlas()
	srcW := AtlasCellW
	if atlasWideFlags[rank] == 1 {
		srcW = 2 * AtlasCellW
	}
	bitIdx := int(atlasOffsets[rank]) + row*srcW + col
	b := atlasPixels[bitIdx>>3]
	return (b >> (7 - (bitIdx & 7))) & 1
}

func atlasGrayByte(rank, row, col int) uint8 {
	loadAtlas()
	srcW := AtlasGrayCellW
	if atlasGrayWideFlags[rank] == 1 {
		srcW = 2 * AtlasGrayCellW
	}
	byteIdx := int(atlasGrayOffsets[rank]) + row*srcW + col
	return atlasGrayPixels[byteIdx]
}
