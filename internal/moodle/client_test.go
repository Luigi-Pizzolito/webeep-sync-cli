package moodle

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testClient(t *testing.T, handler http.Handler) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c := NewClient(srv.URL, "testtoken0123456789testtoken012345678")
	c.RetryDelay = 10 * time.Millisecond
	return c, srv
}

func jsonHandler(status int, body string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	})
}

func TestCallInvalidToken(t *testing.T) {
	c, _ := testClient(t, jsonHandler(http.StatusUnauthorized,
		`{"exception":"core\\exception\\invalidtoken_exception","errorcode":"invalidtoken","message":"Invalid token - token not found"}`))
	_, err := c.GetSiteInfo(context.Background())
	if !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("err = %v, want ErrInvalidToken", err)
	}
}

func TestCallRetriesTransientErrors(t *testing.T) {
	var hits int
	mux := http.NewServeMux()
	mux.HandleFunc("/webservice/rest/server.php", func(w http.ResponseWriter, r *http.Request) {
		hits++
		if hits < 3 {
			http.Error(w, "upstream down", http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `[{"id":42,"fullname":"1 - X (y)","shortname":"X","displayname":"X"}]`)
	})
	c, _ := testClient(t, mux)
	courses, err := c.GetUserCourses(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if hits != 3 {
		t.Fatalf("expected 3 attempts, got %d", hits)
	}
	if len(courses) != 1 || courses[0].ID != 42 || courses[0].FullName != "1 - X (y)" {
		t.Fatalf("bad decode: %+v", courses)
	}
}

func TestCallGivesUpAfterRetries(t *testing.T) {
	c, _ := testClient(t, jsonHandler(http.StatusInternalServerError, "boom"))
	_, err := c.GetSiteInfo(context.Background())
	if err == nil || !strings.Contains(err.Error(), "500") {
		t.Fatalf("err = %v, want an HTTP 500 failure after retries", err)
	}
}

func TestCallHonoursContextCancellation(t *testing.T) {
	c, _ := testClient(t, jsonHandler(http.StatusInternalServerError, "boom"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := c.GetSiteInfo(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestGetSiteInfo(t *testing.T) {
	c, _ := testClient(t, jsonHandler(http.StatusOK,
		`{"userid":123,"sitename":"WeBeep","fullname":"Mario Rossi","username":"mrossi"}`))
	info, err := c.GetSiteInfo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info.UserID != 123 || info.FullName != "Mario Rossi" {
		t.Fatalf("bad decode: %+v", info)
	}
}

func TestGetCourseContents(t *testing.T) {
	body := `[{"id":1,"name":"Materiali","modules":[{"id":2,"name":"F","modname":"folder","contents":[{"type":"file","filename":"a.pdf","filepath":"/","filesize":3,"fileurl":"http://x/a","timecreated":1,"timemodified":2}]}]}]`
	c, _ := testClient(t, jsonHandler(http.StatusOK, body))
	sections, err := c.GetCourseContents(context.Background(), 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(sections) != 1 || len(sections[0].Modules) != 1 {
		t.Fatalf("bad decode: %+v", sections)
	}
	if sections[0].Modules[0].Contents[0].FileURL != "http://x/a" {
		t.Fatalf("bad fileurl: %+v", sections[0].Modules[0].Contents[0])
	}
}

func TestTokenViaSessionCookie(t *testing.T) {
	blob := base64.StdEncoding.EncodeToString([]byte("uasdk:::" + fakeToken + ":::priv"))
	var gotCookie string
	mux := http.NewServeMux()
	mux.HandleFunc("/admin/tool/mobile/launch.php", func(w http.ResponseWriter, r *http.Request) {
		gotCookie = r.Header.Get("Cookie")
		w.Header().Set("Location", "moodlemobile://token="+blob)
		w.WriteHeader(http.StatusSeeOther)
	})
	c, _ := testClient(t, mux)
	tok, err := c.TokenViaSessionCookie(context.Background(), "MoodleSession=abc123")
	if err != nil {
		t.Fatal(err)
	}
	if tok != fakeToken {
		t.Fatalf("token = %q, want %q", tok, fakeToken)
	}
	if gotCookie != "MoodleSession=abc123" {
		t.Fatalf("cookie not forwarded: %q", gotCookie)
	}
}

func TestTokenViaSessionCookieFollowsHTTPRedirects(t *testing.T) {
	blob := base64.StdEncoding.EncodeToString([]byte("uasdk:::" + fakeToken + ":::priv"))
	mux := http.NewServeMux()
	mux.HandleFunc("/admin/tool/mobile/launch.php", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/middle.php", http.StatusFound)
	})
	mux.HandleFunc("/middle.php", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "moodlemobile://token="+blob, http.StatusSeeOther)
	})
	c, _ := testClient(t, mux)
	tok, err := c.TokenViaSessionCookie(context.Background(), "MoodleSession=abc123")
	if err != nil {
		t.Fatal(err)
	}
	if tok != fakeToken {
		t.Fatalf("token = %q, want %q", tok, fakeToken)
	}
}

func TestTokenViaSessionCookieNoRedirect(t *testing.T) {
	c, _ := testClient(t, jsonHandler(http.StatusOK, `<html><body>Login</body></html>`))
	_, err := c.TokenViaSessionCookie(context.Background(), "MoodleSession=expired")
	if err == nil || !strings.Contains(err.Error(), "no mobile token") {
		t.Fatalf("err = %v, want a 'no mobile token' error", err)
	}
}

func TestTokenViaPasswordLogin(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/login/token.php", func(w http.ResponseWriter, r *http.Request) {
		if r.FormValue("service") != "moodle_mobile_app" {
			http.Error(w, "bad service", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"token":"`+fakeToken+`","privatetoken":""}`)
	})
	c, _ := testClient(t, mux)
	tok, err := c.TokenViaPasswordLogin(context.Background(), "user", "pass")
	if err != nil {
		t.Fatal(err)
	}
	if tok != fakeToken {
		t.Fatalf("token = %q", tok)
	}
}

func TestTokenViaPasswordLoginRejected(t *testing.T) {
	c, _ := testClient(t, jsonHandler(http.StatusOK,
		`{"error":"No permission to create web service token for the service moodle_mobile_app.","errorcode":"nopermissions"}`))
	_, err := c.TokenViaPasswordLogin(context.Background(), "user", "pass")
	if err == nil || !strings.Contains(err.Error(), "nopermissions") && !strings.Contains(err.Error(), "No permission") {
		t.Fatalf("err = %v, want the server's rejection message", err)
	}
}

func TestDownloadFileWritesAtomicallyWithToken(t *testing.T) {
	dir := t.TempDir()
	var gotToken string
	mux := http.NewServeMux()
	mux.HandleFunc("/pluginfile.php/1/mod_resource/file/0/Lecture/lec1.pdf", func(w http.ResponseWriter, r *http.Request) {
		gotToken = r.URL.Query().Get("token")
		fmt.Fprint(w, "hello lecture")
	})
	c, _ := testClient(t, mux)
	dst := filepath.Join(dir, "Corso", "lec1.pdf")
	n, err := c.DownloadFile(context.Background(),
		c.BaseURL+"/pluginfile.php/1/mod_resource/file/0/Lecture/lec1.pdf?some=1", dst)
	if err != nil {
		t.Fatal(err)
	}
	if gotToken != c.Token {
		t.Fatalf("token query param = %q, want %q", gotToken, c.Token)
	}
	b, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "hello lecture" || n != int64(len(b)) {
		t.Fatalf("content = %q, n = %d", b, n)
	}
	leftovers, _ := filepath.Glob(filepath.Join(dir, "Corso", ".webeep-part-*"))
	if len(leftovers) != 0 {
		t.Fatalf("leftover temp files: %v", leftovers)
	}
}

func TestDownloadFileNotFound(t *testing.T) {
	c, _ := testClient(t, http.NotFoundHandler())
	_, err := c.DownloadFile(context.Background(), c.BaseURL+"/gone", filepath.Join(t.TempDir(), "gone"))
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestDownloadFileRetries500(t *testing.T) {
	var hits int
	mux := http.NewServeMux()
	mux.HandleFunc("/flaky", func(w http.ResponseWriter, r *http.Request) {
		hits++
		if hits < 3 {
			http.Error(w, "busy", http.StatusBadGateway)
			return
		}
		fmt.Fprint(w, "ok")
	})
	c, _ := testClient(t, mux)
	dst := filepath.Join(t.TempDir(), "flaky.bin")
	if _, err := c.DownloadFile(context.Background(), c.BaseURL+"/flaky", dst); err != nil {
		t.Fatal(err)
	}
	if hits != 3 {
		t.Fatalf("hits = %d, want 3", hits)
	}
}

func TestDownloadFileDetectsRejectedToken(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/pluginfile.php/1/x", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/login/index.php", http.StatusFound)
	})
	mux.HandleFunc("/login/index.php", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "<html>please login</html>")
	})
	c, _ := testClient(t, mux)
	_, err := c.DownloadFile(context.Background(), c.BaseURL+"/pluginfile.php/1/x", filepath.Join(t.TempDir(), "x"))
	if !errors.Is(err, ErrTokenRejected) {
		t.Fatalf("err = %v, want ErrTokenRejected", err)
	}
}

func TestAPIErrorMessage(t *testing.T) {
	e := &APIError{Errorcode: "nopermissions", Message: "no permission", StatusCode: 403}
	if !strings.Contains(e.Error(), "nopermissions") || !strings.Contains(e.Error(), "no permission") {
		t.Fatalf("Error() = %q", e.Error())
	}
	if errors.Is(e, ErrInvalidToken) {
		t.Fatal("nopermissions should not unwrap to ErrInvalidToken")
	}
	e2 := &APIError{Errorcode: "invalidtoken", StatusCode: 401}
	if !errors.Is(e2, ErrInvalidToken) {
		t.Fatal("invalidtoken should unwrap to ErrInvalidToken")
	}
}
