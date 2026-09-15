package providers

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
)

// ErrGoogleRequestCredentials contains no caller values and is safe to return
// when equivalent Google authentication inputs disagree or are malformed.
var ErrGoogleRequestCredentials = errors.New("Google request credentials are invalid or conflicting")

// GoogleRequestAPIKey resolves the credential spellings Google documents: the
// x-goog-api-key header and the key/$key system parameters. Nothing else counts
// as a Google credential — in particular x-api-key is another provider's header,
// so it neither conflicts with these nor selects a Google account here.
//
//	https://cloud.google.com/apis/docs/system-parameters
//	https://ai.google.dev/gemini-api/docs/api-key
func GoogleRequestAPIKey(req *http.Request) (string, error) {
	key := strings.TrimSpace(req.Header.Get("x-goog-api-key"))
	if strings.ContainsAny(key, "\r\n") {
		return "", ErrGoogleRequestCredentials
	}
	for _, part := range strings.Split(req.URL.RawQuery, "&") {
		name, value, _ := strings.Cut(part, "=")
		name, _ = url.QueryUnescape(name)
		if name != "key" && name != "$key" {
			continue
		}
		value, err := url.QueryUnescape(value)
		if err != nil || strings.ContainsAny(value, "\r\n") {
			return "", ErrGoogleRequestCredentials
		}
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if key != "" && key != value {
			return "", ErrGoogleRequestCredentials
		}
		key = value
	}
	return key, nil
}

// WithoutGoogleAPIKeyQuery removes only authentication parameters. Other query
// bytes and their order remain unchanged, including repeated parameters.
func WithoutGoogleAPIKeyQuery(rawQuery string) string {
	parts := strings.Split(rawQuery, "&")
	kept := parts[:0]
	for _, part := range parts {
		name, _, _ := strings.Cut(part, "=")
		name, _ = url.QueryUnescape(name)
		if name != "key" && name != "$key" {
			kept = append(kept, part)
		}
	}
	return strings.Join(kept, "&")
}

// oauthQueryParameters are the discouraged-but-documented spellings for an
// OAuth token in the URL (RFC 6750 §2.3; Google's OAuth 2.0 guide).
var oauthQueryParameters = [...]string{"access_token", "oauth_token"}

// GoogleRequestCarriesQueryCredential reports a caller credential supplied as a
// URL query parameter that this proxy does not resolve. The request already
// names a principal, so no environment credential may be added beside it.
func GoogleRequestCarriesQueryCredential(req *http.Request) bool {
	for _, part := range strings.Split(req.URL.RawQuery, "&") {
		name, value, _ := strings.Cut(part, "=")
		name, _ = url.QueryUnescape(name)
		for _, candidate := range oauthQueryParameters {
			if name == candidate && strings.TrimSpace(value) != "" {
				return true
			}
		}
	}
	return false
}
