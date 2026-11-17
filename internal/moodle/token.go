package moodle

import (
	"encoding/base64"
	"errors"
	"net/url"
	"strings"
)

// ParseToken accepts any of the values a user can realistically copy out of a
// browser and extracts the Moodle web service token:
//
//   - the raw token from the "Access keys" page or a token manager
//   - the base64 blob carried by the moodlemobile:// redirect
//   - the full moodlemobile://...token=<blob> URL itself
func ParseToken(input string) (string, error) {
	s := strings.TrimSpace(input)
	if s == "" {
		return "", errors.New("empty token")
	}
	if i := strings.Index(s, "token="); i >= 0 {
		s = s[i+len("token="):]
		if j := strings.IndexAny(s, "&\"' \t"); j >= 0 {
			s = s[:j]
		}
		if unescaped, err := url.QueryUnescape(s); err == nil {
			s = unescaped
		}
	}
	return extractToken(s)
}

// extractToken tries base64 blob decoding first (format "uasdk:::token:::private"),
// then falls back to treating the input as a raw token.
func extractToken(s string) (string, error) {
	for _, enc := range []*base64.Encoding{
		base64.StdEncoding, base64.URLEncoding,
		base64.RawStdEncoding, base64.RawURLEncoding,
	} {
		decoded, err := enc.DecodeString(s)
		if err != nil {
			continue
		}
		for _, part := range strings.Split(string(decoded), ":::") {
			if looksLikeToken(part) {
				return part, nil
			}
		}
	}
	if looksLikeToken(s) {
		return s, nil
	}
	return "", errors.New("no usable web service token found in the provided value")
}

// looksLikeToken matches Moodle web service tokens: random alphanumeric
// strings (random_alphanumeric() produces 32 characters).
func looksLikeToken(s string) bool {
	if len(s) < 16 || len(s) > 64 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		default:
			return false
		}
	}
	return true
}
