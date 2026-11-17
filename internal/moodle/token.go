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

// extractToken decodes the base64 blob from launch.php by position, the way
// the Moodle app and the original WeBeep Sync do. WeBeep sends
// "uasdk:::token:::privatetoken" where uasdk is an md5 of the browser user
// agent: it looks exactly like a token, so scanning for the first
// token-shaped field silently picks the wrong one. Newer Moodle variants
// send "token:::privatetoken:::"; position rules cover both.
func extractToken(s string) (string, error) {
	for _, enc := range []*base64.Encoding{
		base64.StdEncoding, base64.URLEncoding,
		base64.RawStdEncoding, base64.RawURLEncoding,
	} {
		decoded, err := enc.DecodeString(s)
		if err != nil {
			continue
		}
		if tok, ok := tokenFromBlob(string(decoded)); ok {
			return tok, nil
		}
	}
	if looksLikeToken(s) {
		return s, nil
	}
	return "", errors.New("no usable web service token found in the provided value")
}

// tokenFromBlob picks the web service token out of a launch.php blob:
//   - uasdk:::token:::privatetoken (3 fields) -> middle field
//   - token:::privatetoken        (2 fields) -> first field
//
// The trailing-empty form "token:::privatetoken:::" collapses to 2 fields.
func tokenFromBlob(blob string) (string, bool) {
	if !strings.Contains(blob, ":::") {
		return "", false
	}
	parts := strings.Split(blob, ":::")
	for len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	if len(parts) < 2 {
		if len(parts) == 1 && looksLikeToken(parts[0]) {
			return parts[0], true
		}
		return "", false
	}
	if cand := parts[len(parts)-2]; looksLikeToken(cand) {
		return cand, true
	}
	// Position rule failed (unusual site config): scan every field except the
	// last one, which is the private token.
	for _, p := range parts[:len(parts)-1] {
		if looksLikeToken(p) {
			return p, true
		}
	}
	return "", false
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
