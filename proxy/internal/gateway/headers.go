package gateway

import (
	"net/http"
	"strings"
)

var fixedHopByHopHeaders = map[string]struct{}{
	"connection":          {},
	"keep-alive":          {},
	"proxy-authenticate":  {},
	"proxy-authorization": {},
	"proxy-connection":    {},
	"te":                  {},
	"trailer":             {},
	"transfer-encoding":   {},
	"upgrade":             {},
}

// connectionTokens returns fields nominated by Connection. RFC 9110 makes
// those fields hop-by-hop even when their names are otherwise application-safe.
func connectionTokens(headers http.Header) map[string]struct{} {
	out := make(map[string]struct{})
	for _, value := range headers.Values("Connection") {
		for _, token := range strings.Split(value, ",") {
			if token = strings.ToLower(strings.TrimSpace(token)); token != "" {
				out[token] = struct{}{}
			}
		}
	}
	return out
}

func unsafeForwardHeader(name string, nominated map[string]struct{}) bool {
	lower := strings.ToLower(strings.TrimSpace(name))
	if lower == "" || lower == "x-cave" || lower == "x-caveman" ||
		strings.HasPrefix(lower, "x-cave-") || strings.HasPrefix(lower, "x-caveman-") {
		return true
	}
	if _, blocked := fixedHopByHopHeaders[lower]; blocked {
		return true
	}
	_, blocked := nominated[lower]
	return blocked
}

// chatGPTRequestHeaders preserves OAuth/account and ordinary application
// headers while removing proxy-private and hop-by-hop fields.
func chatGPTRequestHeaders(headers http.Header) http.Header {
	out := make(http.Header)
	nominated := connectionTokens(headers)
	for name, values := range headers {
		if unsafeForwardHeader(name, nominated) {
			continue
		}
		out[name] = append([]string(nil), values...)
	}
	return out
}

func copySafeResponseHeaders(dst, src http.Header) {
	nominated := connectionTokens(src)
	for name, values := range src {
		if unsafeForwardHeader(name, nominated) {
			continue
		}
		for _, value := range values {
			dst.Add(name, value)
		}
	}
}
