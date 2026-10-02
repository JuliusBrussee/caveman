package gateway

import (
	"strings"

	"github.com/klauspost/compress/zstd"
)

type chatGPTRequestEncoding string

const (
	chatGPTRequestIdentity chatGPTRequestEncoding = ""
	chatGPTRequestZstd     chatGPTRequestEncoding = "zstd"
)

func decodeChatGPTRequestBody(wire []byte, contentEncoding string) ([]byte, chatGPTRequestEncoding, bool) {
	switch encoding := chatGPTRequestEncoding(strings.ToLower(strings.TrimSpace(contentEncoding))); encoding {
	case chatGPTRequestIdentity:
		return wire, encoding, true
	case chatGPTRequestZstd:
		decoder, err := zstd.NewReader(
			nil,
			zstd.WithDecoderConcurrency(1),
			zstd.WithDecoderMaxWindow(uint64(chatGPTCaptureLimit*2)),
			zstd.WithDecoderMaxMemory(uint64(chatGPTCaptureLimit*8)),
		)
		if err != nil {
			return nil, encoding, false
		}
		defer decoder.Close()
		decoded, err := decoder.DecodeAll(wire, nil)
		if err != nil || len(decoded) > chatGPTCaptureLimit {
			return nil, encoding, false
		}
		return decoded, encoding, true
	default:
		return nil, encoding, false
	}
}

func encodeChatGPTRequestBody(logical []byte, encoding chatGPTRequestEncoding) ([]byte, bool) {
	switch encoding {
	case chatGPTRequestIdentity:
		return logical, true
	case chatGPTRequestZstd:
		encoder, err := zstd.NewWriter(
			nil,
			zstd.WithEncoderConcurrency(1),
			zstd.WithEncoderLevel(zstd.EncoderLevelFromZstd(3)),
		)
		if err != nil {
			return nil, false
		}
		defer encoder.Close()
		return encoder.EncodeAll(logical, nil), true
	default:
		return nil, false
	}
}
