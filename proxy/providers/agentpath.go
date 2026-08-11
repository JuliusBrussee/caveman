package providers

import "strings"

// SplitAgentPath splits a /w/<slug>/... attribution path.
func SplitAgentPath(path string) (slug, rest string, ok bool) {
	if !strings.HasPrefix(path, "/w/") {
		return "", "", false
	}
	trimmed := strings.TrimPrefix(path, "/w/")
	slash := strings.IndexByte(trimmed, '/')
	if slash <= 0 {
		return "", "", false
	}
	slug = trimmed[:slash]
	rest = trimmed[slash:]
	if rest == "/" || !validAgentSlug(slug) {
		return "", "", false
	}
	return slug, rest, true
}

func validAgentSlug(slug string) bool {
	if len(slug) == 0 || len(slug) > 64 {
		return false
	}
	for i := 0; i < len(slug); i++ {
		c := slug[i]
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' {
			continue
		}
		if i > 0 && (c == '.' || c == '_' || c == '-') {
			continue
		}
		return false
	}
	return true
}
