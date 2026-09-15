package engine_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/JuliusBrussee/caveman/engine"
)

func TestMalformedToolSchemaSuffixPassesThroughExactBytes(t *testing.T) {
	eng := newEngine(t)
	catalog := `{"type":"object","title":"` + strings.Repeat("discardable annotation ", 50) + `","properties":{"id":{"type":"integer"}}}`
	valid, err := eng.Compress([]byte(catalog), engine.Options{Mode: engine.ModeCompress, Type: "toolschema"})
	if err != nil || valid.RecoveryHandle == "" || valid.Ratio <= 0 {
		t.Fatalf("valid fixture must exercise successful lossy compression: %+v err=%v", valid, err)
	}
	for _, suffix := range []string{"}", "]", "\n }", "\n ]"} {
		input := []byte(catalog + suffix)
		result, err := eng.Compress(input, engine.Options{Mode: engine.ModeCompress, Type: "toolschema"})
		if err != nil || !bytes.Equal(result.Output, input) || result.RecoveryHandle != "" || result.Ratio != 0 || result.TokensBefore != result.TokensAfter {
			t.Fatalf("suffix=%q must preserve every byte without savings or recovery: %+v err=%v", suffix, result, err)
		}
	}
}

func TestMalformedJSONSuffixPassesThroughExactBytes(t *testing.T) {
	eng := newEngine(t)
	payload := `{"rows":[` + strings.Repeat(`{"id":1,"state":"ok"},`, 50) + `{"id":1,"state":"ok"}]}`
	valid, err := eng.Compress([]byte(payload), engine.Options{Mode: engine.ModeCompress, Type: "json"})
	if err != nil || valid.RecoveryHandle == "" || valid.Ratio <= 0 {
		t.Fatalf("valid fixture must exercise successful array elision: %+v err=%v", valid, err)
	}
	for _, suffix := range []string{"}", "]", "\n }", "\n ]", " {}", " invalid"} {
		input := []byte(payload + suffix)
		result, err := eng.Compress(input, engine.Options{Mode: engine.ModeCompress, Type: "json"})
		if err != nil || !bytes.Equal(result.Output, input) || result.RecoveryHandle != "" || result.Ratio != 0 || result.TokensBefore != result.TokensAfter {
			t.Fatalf("suffix=%q must preserve every byte without savings or recovery: %+v err=%v", suffix, result, err)
		}
	}
}
