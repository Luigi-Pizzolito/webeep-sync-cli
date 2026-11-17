package moodle

import (
	"encoding/base64"
	"net/url"
	"strings"
	"testing"
)

const fakeToken = "Ab3dEf0123456789Ab3dEf0123456789"

func blob(t string) string {
	return base64.StdEncoding.EncodeToString([]byte("uasdk:::" + t + ":::privatetok"))
}

func TestParseTokenMoodlemobileURL(t *testing.T) {
	in := "moodlemobile://token=" + blob(fakeToken) + "&extra=1"
	got, err := ParseToken(in)
	if err != nil {
		t.Fatal(err)
	}
	if got != fakeToken {
		t.Fatalf("got %q want %q", got, fakeToken)
	}
}

func TestParseTokenURLEncoded(t *testing.T) {
	in := "moodlemobile://token=" + url.QueryEscape(blob(fakeToken))
	got, err := ParseToken(in)
	if err != nil {
		t.Fatal(err)
	}
	if got != fakeToken {
		t.Fatalf("got %q want %q", got, fakeToken)
	}
}

func TestParseTokenBlobDirect(t *testing.T) {
	got, err := ParseToken(blob(fakeToken))
	if err != nil {
		t.Fatal(err)
	}
	if got != fakeToken {
		t.Fatalf("got %q want %q", got, fakeToken)
	}
}

func TestParseTokenRaw(t *testing.T) {
	got, err := ParseToken("  " + fakeToken + "  ")
	if err != nil {
		t.Fatal(err)
	}
	if got != fakeToken {
		t.Fatalf("got %q want %q", got, fakeToken)
	}
}

func TestParseTokenGarbage(t *testing.T) {
	for _, in := range []string{"", "   ", "not a token", "https://webeep.polimi.it/login/index.php"} {
		if _, err := ParseToken(in); err == nil {
			t.Errorf("ParseToken(%q) unexpectedly succeeded", in)
		}
	}
}

func TestLooksLikeTokenRejectsURLs(t *testing.T) {
	if looksLikeToken(strings.Repeat("a", 40) + "/x") {
		t.Fatal("path-like string accepted as token")
	}
}
