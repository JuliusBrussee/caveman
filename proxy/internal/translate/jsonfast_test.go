package translate

import "testing"

func TestUnsignedThinkingScan(t *testing.T) {
	for body, want := range map[string]bool{
		`{"signature":""}`:                      true,
		`{"signature" : ""}`:                    true,
		`{"data":""}`:                           true,
		`{"thinking":"","signature":"sig"}`:     false,
		`{"text":"say \"\" here"}`:              false,
		`{"text":"x\""}`:                        false,
		`{"metadata":""}`:                       false,
		`{"a":"","b":{"data": "" }}`:            true,
		`no quotes at all`:                      false,
		`{"text":"\"signature\":\"\" in text"}`: false,
	} {
		if got := unsignedThinking([]byte(body)); got != want {
			t.Errorf("unsignedThinking(%s) = %v", body, got)
		}
	}
}

// The reader agrees with encoding/json on what is a value and where it ends.
func TestReaderSplitsLikeEncodingJSON(t *testing.T) {
	body := []byte(` { "a" : [1, -2.5e3, true, null, "x\"y\\", {"b":[]}] , "cA":{} } `)
	fields, err := topFields(body)
	if err != nil || len(fields) != 2 || string(fields["a"]) != `[1, -2.5e3, true, null, "x\"y\\", {"b":[]}]` || string(fields["cA"]) != `{}` {
		t.Fatalf("fields = %q, %v", fields, err)
	}
	if got := items(fields["a"]); len(got) != 6 || string(got[4]) != `"x\"y\\"` || jstr(got[4]) != `x"y\` {
		t.Fatalf("items = %q", got)
	}
	for _, bad := range []string{`{"a":}`, `{"a" 1}`, `{"a":1,}`, `[1 2]`, `{"a":"unterminated}`, `{"a":tru}`} {
		if _, err := topFields([]byte(bad)); err == nil && bad[0] == '{' {
			t.Errorf("accepted %s", bad)
		}
	}
	if got := string(appendString(nil, "a\"b\\c\n\x01\xff\U0001F600")); got != "\"a\\\"b\\\\c\\n\\u0001\\ufffd\U0001F600\"" {
		t.Fatalf("appendString = %q", got)
	}
}
