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

// WeBeep's real blob shape: uasdk:::token:::privatetoken. The uasdk is an md5
// hex string and looks exactly like a token; the wrong pick made login fail.
func TestParseTokenWeBeepThreeFieldBlob(t *testing.T) {
	uasdk := "0123456789abcdef0123456789abcdef"
	private := strings.Repeat("p", 64)
	b := base64.StdEncoding.EncodeToString([]byte(uasdk + ":::" + fakeToken + ":::" + private))
	got, err := ParseToken("moodlemobile://token=" + b)
	if err != nil {
		t.Fatal(err)
	}
	if got != fakeToken {
		t.Fatalf("picked the wrong field: got %q want %q", got, fakeToken)
	}
}

func TestParseTokenTwoFieldBlobVariants(t *testing.T) {
	private := strings.Repeat("q", 64)
	for _, shape := range []string{
		fakeToken + ":::" + private,
		fakeToken + ":::" + private + ":::",
	} {
		b := base64.StdEncoding.EncodeToString([]byte(shape))
		got, err := ParseToken(b)
		if err != nil {
			t.Fatalf("shape %q: %v", shape, err)
		}
		if got != fakeToken {
			t.Fatalf("shape %q: got %q want %q", shape, got, fakeToken)
		}
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
