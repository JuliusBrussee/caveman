// Ported from pxpipe (https://github.com/teamchong/pxpipe), MIT License, Copyright (c) 2026 claude-image-proxy contributors.

package pixel

import (
	"math"
	"strings"
	"testing"
)

func TestGateAntiFlap(t *testing.T) {
	if !IsCompressionProfitable(strings.Repeat("x", 50000), 100, 0, 2, 4, 0, 0, true, ReadableCharsPerImage, conservativeStdParams) {
		t.Fatal("clearly-profitable cold case rejected")
	}

	text := strings.Repeat("x", 12000)
	cold := IsCompressionProfitable(text, 100, 0, 2, 4, 0, 0, true, ReadableCharsPerImage, conservativeStdParams)
	warmText := IsCompressionProfitable(text, 100, 0, 2, 4, 50000, 0, true, ReadableCharsPerImage, conservativeStdParams)
	if cold && warmText {
		t.Fatal("warm text side did not pin session in text mode")
	}

	cheapText := strings.Repeat("hello", 500)
	if IsCompressionProfitable(cheapText, 100, 0, 1, 50, 0, 0, true, ReadableCharsPerImage, conservativeStdParams) {
		t.Fatal("cold cheap-text case accepted")
	}
	if !IsCompressionProfitable(cheapText, 100, 0, 1, 50, 0, 60000, true, ReadableCharsPerImage, conservativeStdParams) {
		t.Fatal("warm image side did not pin session in image mode")
	}

	both := IsCompressionProfitable(text, 100, 0, 2, 4, 25000, 25000, true, ReadableCharsPerImage, conservativeStdParams)
	if both != cold {
		t.Fatalf("symmetric burn changed verdict: both=%v cold=%v", both, cold)
	}
}

func TestEvalCompressionProfitability(t *testing.T) {
	samples := []struct {
		text     string
		warmText float64
		warmImg  float64
	}{
		{strings.Repeat("x", 50000), 0, 0},
		{strings.Repeat("x", 12000), 50000, 0},
		{strings.Repeat("hello", 500), 0, 60000},
		{strings.Repeat("x", 12000), 25000, 25000},
	}
	for _, sample := range samples {
		e := EvalCompressionProfitability(sample.text, 100, 0, 2, 4, sample.warmText, sample.warmImg, true, conservativeStdParams)
		if e == nil {
			t.Fatal("EvalCompressionProfitability returned nil")
		}
		verdict := IsCompressionProfitable(sample.text, 100, 0, 2, 4, sample.warmText, sample.warmImg, true, ReadableCharsPerImage, conservativeStdParams)
		if e.Profitable != verdict {
			t.Fatalf("eval verdict = %v, want %v", e.Profitable, verdict)
		}
	}

	e := EvalCompressionProfitability(strings.Repeat("x", 12000), 100, 0, 2, 4, 40000, 60000, true, conservativeStdParams)
	if e == nil {
		t.Fatal("nil eval")
	}
	if math.Abs(e.BurnImageSide-40000*1.15) > 0.00001 {
		t.Fatalf("BurnImageSide = %f", e.BurnImageSide)
	}
	if math.Abs(e.BurnTextSide-60000*1.15) > 0.00001 {
		t.Fatalf("BurnTextSide = %f", e.BurnTextSide)
	}
	if e.ImageTokens <= 0 || e.TextTokens <= 0 {
		t.Fatalf("non-positive token terms: %+v", e)
	}
	if EvalCompressionProfitability("", 100, 0, 1, 4, 0, 0, true, conservativeStdParams) != nil {
		t.Fatal("empty text eval should be nil")
	}
}

func TestGateAmortizedAndMaxChars(t *testing.T) {
	text := strings.Repeat("x", 50000)
	cold := IsCompressionProfitableAmortized(text, 100, 0, 2, 4, 1, 0, 0, true, ReadableCharsPerImage, conservativeStdParams)
	base := IsCompressionProfitable(text, 100, 0, 2, 4, 0, 0, true, ReadableCharsPerImage, conservativeStdParams)
	if cold != base {
		t.Fatalf("horizon=1 = %v, want base %v", cold, base)
	}
	if MaxCharsPerImage(100) != 9000 {
		t.Fatalf("MaxCharsPerImage(100) = %d, want 9000", MaxCharsPerImage(100))
	}
	if MaxCharsPerImage(313) != ReadableCharsPerImage {
		t.Fatalf("MaxCharsPerImage(313) = %d, want %d", MaxCharsPerImage(313), ReadableCharsPerImage)
	}
}
