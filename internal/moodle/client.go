// Package moodle is a small client for the Moodle web service API exposed by
// WeBeep (webeep.polimi.it). It handles REST calls, token retrieval and
// authenticated file downloads.
package moodle

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Client talks to the web service endpoints of one Moodle/WeBeep site.
type Client struct {
	BaseURL string
	Token   string

	// RetryDelay is the base delay for exponential backoff on transient
	// errors; it is exported so tests can shrink it.
	RetryDelay time.Duration

	HTTP     *http.Client // API calls
	Download *http.Client // streaming downloads (no global timeout)
}

func NewClient(baseURL, token string) *Client {
	baseURL = strings.TrimSuffix(baseURL, "/")
	return &Client{
		BaseURL:    baseURL,
		Token:      token,
		RetryDelay: time.Second,
		HTTP:       &http.Client{Timeout: 60 * time.Second},
		Download: &http.Client{
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				// A download that would land on the login page means the
				// token was rejected: stop and surface it as such.
				if strings.Contains(req.URL.Path, "/login/index.php") {
					return ErrTokenRejected
				}
				if len(via) >= 10 {
					return errors.New("too many redirects while downloading")
				}
				return nil
			},
		},
	}
}

func (c *Client) userAgent() string {
	return "webeep-sync-cli (Moodle REST client; based on toto04/webeep-sync)"
}

// APIError is a Moodle web service error response.
type APIError struct {
	Exception  string `json:"exception"`
	Errorcode  string `json:"errorcode"`
	Message    string `json:"message"`
	Debuginfo  string `json:"debuginfo"`
	StatusCode int    `json:"-"`
}

func (e *APIError) Error() string {
	msg := e.Message
	if msg == "" {
		msg = e.Debuginfo
	}
	if msg == "" {
		msg = fmt.Sprintf("HTTP %d", e.StatusCode)
	}
	return fmt.Sprintf("moodle error %q: %s", e.Errorcode, msg)
}

// Unwrap maps the "invalidtoken" error code to ErrInvalidToken so callers can
// detect an expired or revoked token with errors.Is.
func (e *APIError) Unwrap() error {
	if e.Errorcode == "invalidtoken" {
		return ErrInvalidToken
	}
	return nil
}

// ErrInvalidToken marks a rejected, expired or revoked web service token.
var ErrInvalidToken = errors.New("invalid or expired token")

// Call invokes one web service function and returns the raw JSON payload.
// Transient failures (network errors, HTTP 5xx) are retried with exponential
// backoff; Moodle-level errors are returned as *APIError.
func (c *Client) Call(ctx context.Context, wsfunction string, params map[string]string) ([]byte, error) {
	form := url.Values{
		"wstoken":               {c.Token},
		"wsfunction":            {wsfunction},
		"moodlewsrestformat":    {"json"},
		"moodlewssettingfilter": {"true"},
	}
	for k, v := range params {
		form.Set(k, v)
	}
	endpoint := c.BaseURL + "/webservice/rest/server.php"

	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			if err := sleepBackoff(ctx, c.RetryDelay<<attempt); err != nil {
				return nil, err
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("User-Agent", c.userAgent())

		resp, err := c.HTTP.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("%s: %w", wsfunction, err)
			continue
		}
		body, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			lastErr = fmt.Errorf("%s: reading response: %w", wsfunction, readErr)
			continue
		}
		if resp.StatusCode >= 500 {
			lastErr = fmt.Errorf("%s: HTTP %d from %s", wsfunction, resp.StatusCode, endpoint)
			continue
		}
		if api := apiErrorFromBody(resp.StatusCode, body); api != nil {
			return nil, api
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("%s: unexpected HTTP %d from %s", wsfunction, resp.StatusCode, endpoint)
		}
		return body, nil
	}
	return nil, lastErr
}

// apiErrorFromBody reports whether the response body is a Moodle exception.
func apiErrorFromBody(status int, body []byte) *APIError {
	var e APIError
	if err := json.Unmarshal(body, &e); err != nil {
		if status != http.StatusOK {
			return &APIError{Errorcode: fmt.Sprintf("http%d", status), StatusCode: status}
		}
		return nil
	}
	if e.Exception == "" && e.Errorcode == "" {
		return nil
	}
	e.StatusCode = status
	return &e
}

func sleepBackoff(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// SiteInfo is the response of core_webservice_get_site_info.
type SiteInfo struct {
	UserID   int64  `json:"userid"`
	SiteName string `json:"sitename"`
	FullName string `json:"fullname"`
	Username string `json:"username"`
}

func (c *Client) GetSiteInfo(ctx context.Context) (*SiteInfo, error) {
	body, err := c.Call(ctx, "core_webservice_get_site_info", nil)
	if err != nil {
		return nil, err
	}
	var info SiteInfo
	if err := json.Unmarshal(body, &info); err != nil {
		return nil, fmt.Errorf("decoding site info: %w", err)
	}
	if info.UserID == 0 {
		return nil, fmt.Errorf("site info response has no userid")
	}
	return &info, nil
}

// CourseSummary is one enrolled course.
type CourseSummary struct {
	ID          int64  `json:"id"`
	FullName    string `json:"fullname"`
	DisplayName string `json:"displayname"`
	ShortName   string `json:"shortname"`
}

func (c *Client) GetUserCourses(ctx context.Context, userid int64) ([]CourseSummary, error) {
	body, err := c.Call(ctx, "core_enrol_get_users_courses", map[string]string{
		"userid": fmt.Sprintf("%d", userid),
	})
	if err != nil {
		return nil, err
	}
	var courses []CourseSummary
	if err := json.Unmarshal(body, &courses); err != nil {
		return nil, fmt.Errorf("decoding course list: %w", err)
	}
	return courses, nil
}

// Section, ModuleRef and FileContent mirror core_course_get_contents.
type Section struct {
	ID      int64       `json:"id"`
	Name    string      `json:"name"`
	Modules []ModuleRef `json:"modules"`
}

type ModuleRef struct {
	ID       int64         `json:"id"`
	Name     string        `json:"name"`
	ModName  string        `json:"modname"`
	Contents []FileContent `json:"contents"`
}

type FileContent struct {
	Type         string `json:"type"`
	Filename     string `json:"filename"`
	Filepath     string `json:"filepath"`
	Filesize     int64  `json:"filesize"`
	FileURL      string `json:"fileurl"`
	TimeCreated  int64  `json:"timecreated"`
	TimeModified int64  `json:"timemodified"`
}

func (c *Client) GetCourseContents(ctx context.Context, courseID int64) ([]Section, error) {
	body, err := c.Call(ctx, "core_course_get_contents", map[string]string{
		"courseid": fmt.Sprintf("%d", courseID),
	})
	if err != nil {
		return nil, err
	}
	var sections []Section
	if err := json.Unmarshal(body, &sections); err != nil {
		return nil, fmt.Errorf("decoding course contents: %w", err)
	}
	return sections, nil
}

// TokenViaPasswordLogin tries to obtain a mobile web service token through
// /login/token.php. This only works when the site still accepts username and
// password directly; with SPID-only accounts it fails and the caller should
// fall back to a token extracted from a logged-in browser session.
func (c *Client) TokenViaPasswordLogin(ctx context.Context, username, password string) (string, error) {
	form := url.Values{
		"username": {username},
		"password": {password},
		"service":  {"moodle_mobile_app"},
	}
	endpoint := c.BaseURL + "/login/token.php"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", c.userAgent())
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("login/token.php: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("login/token.php: reading response: %w", err)
	}
	var out struct {
		Token     string `json:"token"`
		Privatet  string `json:"privatetoken"`
		Error     string `json:"error"`
		Errorcode string `json:"errorcode"`
		Debuginfo string `json:"debuginfo"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("login/token.php: unexpected response (HTTP %d)", resp.StatusCode)
	}
	if out.Token != "" {
		return out.Token, nil
	}
	msg := out.Error
	if msg == "" {
		msg = out.Debuginfo
	}
	if msg == "" {
		msg = fmt.Sprintf("HTTP %d", resp.StatusCode)
	}
	return "", fmt.Errorf("password login rejected: %s (if you use SPID or AUA single sign-on, get the token from your browser instead: see README)", msg)
}

// TokenViaSessionCookie exchanges browser session cookies for a mobile web
// service token by following the /admin/tool/mobile/launch.php redirect until
// a moodlemobile:// URL shows up.
func (c *Client) TokenViaSessionCookie(ctx context.Context, cookies string) (string, error) {
	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	next := fmt.Sprintf("%s/admin/tool/mobile/launch.php?service=moodle_mobile_app&passport=%d",
		c.BaseURL, time.Now().UnixNano())
	for hop := 0; hop < 8; hop++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, next, nil)
		if err != nil {
			return "", err
		}
		req.Header.Set("Cookie", cookies)
		req.Header.Set("User-Agent", c.userAgent())
		req.Header.Set("Accept", "text/html,application/xhtml+xml")
		resp, err := client.Do(req)
		if err != nil {
			return "", fmt.Errorf("launch.php: %w", err)
		}
		location := resp.Header.Get("Location")
		if strings.Contains(location, "moodlemobile://") {
			resp.Body.Close()
			return ParseToken(location)
		}
		if location != "" {
			if ref, refErr := url.Parse(location); refErr == nil &&
				(ref.Scheme == "" || ref.Scheme == "http" || ref.Scheme == "https") {
				if base, baseErr := url.Parse(next); baseErr == nil {
					next = base.ResolveReference(ref).String()
					resp.Body.Close()
					continue
				}
			}
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if i := strings.Index(string(body), "moodlemobile://"); i >= 0 {
			return ParseToken(string(body[i:]))
		}
		return "", fmt.Errorf("no mobile token returned (HTTP %d at %s): the session cookie is invalid or expired, or WeBeep currently blocks token creation for moodle_mobile_app (see README troubleshooting)", resp.StatusCode, next)
	}
	return "", errors.New("too many redirects while fetching the mobile token")
}

// ErrNotFound marks a file that no longer exists on the server.
var ErrNotFound = errors.New("file not found on server (404)")

// ErrTokenRejected marks a download redirected to the login page, meaning the
// saved token was revoked or expired.
var ErrTokenRejected = errors.New("download was redirected to the login page: the saved token was rejected")

// DownloadFile streams fileURL into dst (atomically), adding the web service
// token to the query like the WeBeep web app does. Transient errors are
// retried; 404 returns ErrNotFound without retrying. It returns the number of
// bytes written. An optional callback receives cumulative bytes while the
// body is streaming, for progress display.
func (c *Client) DownloadFile(ctx context.Context, fileURL, dst string, onProgress ...func(int64)) (int64, error) {
	var progress func(int64)
	if len(onProgress) > 0 {
		progress = onProgress[0]
	}
	u, err := url.Parse(fileURL)
	if err != nil {
		return 0, fmt.Errorf("bad file url %q: %w", fileURL, err)
	}
	q := u.Query()
	q.Set("token", c.Token)
	u.RawQuery = q.Encode()

	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			if err := sleepBackoff(ctx, c.RetryDelay<<attempt); err != nil {
				return 0, err
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			return 0, err
		}
		req.Header.Set("User-Agent", c.userAgent())
		resp, err := c.Download.Do(req)
		if err != nil {
			if errors.Is(err, ErrTokenRejected) {
				return 0, ErrTokenRejected
			}
			if ctxErr := ctx.Err(); ctxErr != nil {
				return 0, ctxErr
			}
			lastErr = fmt.Errorf("GET %s: %w", u.Redacted(), err)
			continue
		}
		if strings.Contains(resp.Request.URL.Path, "/login/index.php") {
			io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
			resp.Body.Close()
			return 0, ErrTokenRejected
		}
		if resp.StatusCode == http.StatusNotFound {
			resp.Body.Close()
			return 0, ErrNotFound
		}
		if resp.StatusCode >= 500 {
			io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
			resp.Body.Close()
			lastErr = fmt.Errorf("GET %s: HTTP %d", u.Redacted(), resp.StatusCode)
			continue
		}
		if resp.StatusCode != http.StatusOK {
			io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
			resp.Body.Close()
			return 0, fmt.Errorf("GET %s: unexpected HTTP %d", u.Redacted(), resp.StatusCode)
		}
		n, writeErr := writeToTemp(resp.Body, dst, progress)
		resp.Body.Close()
		if writeErr != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return 0, ctxErr
			}
			lastErr = writeErr
			continue
		}
		return n, nil
	}
	return 0, lastErr
}

// writeToTemp copies the body into a hidden temporary file in dst's directory
// and renames it into place. When onProgress is non-nil it receives the
// cumulative bytes written, roughly once per chunk.
func writeToTemp(body io.Reader, dst string, onProgress func(int64)) (int64, error) {
	dir := filepath.Dir(dst)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, err
	}
	tmp, err := os.CreateTemp(dir, ".webeep-part-*")
	if err != nil {
		return 0, err
	}
	name := tmp.Name()
	defer os.Remove(name) // no-op after a successful rename
	n, err := io.Copy(tmp, &progressReader{src: body, onProgress: onProgress})
	if err != nil {
		tmp.Close()
		return 0, fmt.Errorf("downloading to disk: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return 0, err
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return 0, err
	}
	if err := tmp.Close(); err != nil {
		return 0, err
	}
	return n, os.Rename(name, dst)
}

// progressReader counts bytes as they stream, reporting at most once per
// progressChunk so a progress display is not updated per network read.
type progressReader struct {
	src        io.Reader
	onProgress func(int64)
	read       int64
	reported   int64
}

// progressChunk is how many bytes may pass between two progress reports.
const progressChunk = 256 * 1024

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.src.Read(b)
	p.read += int64(n)
	if p.onProgress != nil && (err != nil || p.read-p.reported >= progressChunk) {
		p.reported = p.read
		p.onProgress(p.read)
	}
	return n, err
}
