package setupui

import (
	"net/url"
	"strings"
	"unicode"
)

// SafeText returns value if it is free of control characters and credential
// markers, otherwise the replacement. This is the rendering boundary for
// untrusted setup metadata and failure text: no control sequences or
// credential-bearing URLs reach the scene. It mirrors the presentation
// package's campsite sanitizer so both surfaces enforce the same rule.
func SafeText(value, replacement string) string {
	if strings.TrimSpace(value) == "" || unsafeValue(value) {
		return replacement
	}
	return value
}

func unsafeValue(value string) bool {
	for _, r := range value {
		if unicode.IsControl(r) {
			return true
		}
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" {
		return false
	}
	if parsed.User != nil {
		return true
	}
	for key := range parsed.Query() {
		normalized := strings.ToLower(key)
		for _, marker := range []string{"token", "secret", "password", "credential", "access_key"} {
			if strings.Contains(normalized, marker) {
				return true
			}
		}
	}
	return false
}
