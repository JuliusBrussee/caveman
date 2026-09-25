package gateway

import (
	"net/http"
	"testing"
)

func TestChatGPTRequestHeadersRemovePrivateAndHopByHopFields(t *testing.T) {
	in := http.Header{}
	in.Set("Authorization", "Bearer keep")
	in.Set("ChatGPT-Account-ID", "acct_keep")
	in.Set("Connection", "X-Dynamic-Hop")
	in.Set("X-Dynamic-Hop", "drop")
	in.Set("Proxy-Connection", "drop")
	in.Set("X-Caveman-Transform", "drop")
	out := chatGPTRequestHeaders(in)

	if out.Get("Authorization") != "Bearer keep" || out.Get("ChatGPT-Account-ID") != "acct_keep" {
		t.Fatalf("required ChatGPT credentials were removed: %v", out)
	}
	for _, name := range []string{"Connection", "X-Dynamic-Hop", "Proxy-Connection", "X-Caveman-Transform"} {
		if out.Get(name) != "" {
			t.Fatalf("unsafe header %s survived filtering: %v", name, out)
		}
	}
}

func TestRelayedResponseHeadersForbidContentSniffing(t *testing.T) {
	src := http.Header{}
	src.Set("Content-Type", "text/html")
	src.Add("X-Content-Type-Options", "sniff")
	dst := http.Header{}
	copySafeResponseHeaders(dst, src)
	if got := dst.Values("X-Content-Type-Options"); len(got) != 1 || got[0] != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %v, want [nosniff]", got)
	}
	if dst.Get("Content-Type") != "text/html" {
		t.Fatalf("Content-Type was not relayed: %v", dst)
	}
}
