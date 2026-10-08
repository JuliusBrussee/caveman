package anthropic

import (
	"math/rand"
	"testing"
)

// scanJSONStringByteLoop is the byte-at-a-time scan scanJSONString replaced.
func scanJSONStringByteLoop(body []byte, start int) (int, bool) {
	if start >= len(body) || body[start] != '"' {
		return 0, false
	}
	for i := start + 1; i < len(body); i++ {
		switch body[i] {
		case '\\':
			i++
		case '"':
			return i + 1, true
		}
	}
	return 0, false
}

func checkScanJSONString(t *testing.T, body []byte) {
	t.Helper()
	for start := 0; start < len(body); start++ {
		end, ok := scanJSONString(body, start)
		wantEnd, wantOK := scanJSONStringByteLoop(body, start)
		if end != wantEnd || ok != wantOK {
			t.Fatalf("scanJSONString(%q, %d) = %d, %v; want %d, %v", body, start, end, ok, wantEnd, wantOK)
		}
	}
}

// TestScanJSONStringMatchesByteLoop: the IndexByte scan ends every string
// where the byte loop did, escapes and unterminated strings included.
func TestScanJSONStringMatchesByteLoop(t *testing.T) {
	for _, body := range []string{
		``, `"`, `""`, `"a"`, `"a\"b"`, `"a\\"b"`, `"a\\\"b"`, `"\`, `"\\`, `"a\`, `"abc`, `"a\nb\tc"x"`,
		`"\\\\\\\\"`, `"\""`, `"\\""`, `{"k":"v\"","x":"y"}`,
	} {
		checkScanJSONString(t, []byte(body))
	}
	random := rand.New(rand.NewSource(1))
	alphabet := []byte(`"\\ab`)
	for n := 0; n < 20000; n++ {
		body := make([]byte, random.Intn(24))
		for i := range body {
			body[i] = alphabet[random.Intn(len(alphabet))]
		}
		checkScanJSONString(t, body)
	}
}

func FuzzScanJSONString(f *testing.F) {
	f.Add([]byte(`"a\"b\\"c"`))
	f.Fuzz(func(t *testing.T, body []byte) { checkScanJSONString(t, body) })
}
