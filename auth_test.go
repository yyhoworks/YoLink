package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

const authOldPassword = "fixture-old-admin-password"
const authNewPassword = "fixture-new-admin-password"

func authLogin(h http.Handler, password string) *httptest.ResponseRecorder {
	body, _ := json.Marshal(object{"password": password})
	req := httptest.NewRequest("POST", "/api/login", strings.NewReader(string(body)))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func authFixture(t *testing.T) (*fixture, *Server, http.Handler, *http.Cookie) {
	t.Helper()
	f := setup(t)
	t.Setenv("STRMLINK_ADMIN_PASSWORD", authOldPassword)
	s, err := newServer(f.s, f.e, f.c, f.data, f.output)
	if err != nil {
		t.Fatal(err)
	}
	h := s.handler()
	w := authLogin(h, authOldPassword)
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	return f, s, h, w.Result().Cookies()[0]
}

func TestPasswordChangePersistsAndRevokesManagementSessions(t *testing.T) {
	f, s, h, cookie := authFixture(t)
	other := authLogin(h, authOldPassword).Result().Cookies()[0]
	signature := f.s.signature("existing-media")
	oldEventToken := s.currentEventToken()
	w := generationPOST(h, cookie, "/api/password", object{"current_password": authOldPassword, "new_password": authNewPassword, "confirm_password": authNewPassword})
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if s.currentEventToken() == oldEventToken {
		t.Fatal("password change retained the old event token")
	}
	for token, want := range map[string]int{oldEventToken: 401, s.currentEventToken(): 200} {
		req := httptest.NewRequest("GET", "/api/events/catalog", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		r := httptest.NewRecorder()
		h.ServeHTTP(r, req)
		if r.Code != want {
			t.Fatalf("event authentication after password change: got %d, want %d", r.Code, want)
		}
	}
	for _, c := range []*http.Cookie{cookie, other} {
		req := httptest.NewRequest("GET", "/api/state", nil)
		req.AddCookie(c)
		r := httptest.NewRecorder()
		h.ServeHTTP(r, req)
		if r.Code != 401 {
			t.Fatal("old session survived", r.Code)
		}
	}
	if w := authLogin(h, authOldPassword); w.Code != 401 {
		t.Fatal("old password survived", w.Code)
	}
	if w := authLogin(h, authNewPassword); w.Code != 200 {
		t.Fatal("new password rejected", w.Code)
	}
	file := filepath.Join(f.data, "admin-credential.json")
	raw, err := os.ReadFile(file)
	if err != nil || strings.Contains(string(raw), authNewPassword) || strings.Contains(string(raw), authOldPassword) {
		t.Fatal("password storage invalid")
	}
	info, _ := os.Stat(file)
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatal("credential permissions", info.Mode())
	}
	// A saved password wins over bootstrap environment values after restart.
	t.Setenv("STRMLINK_ADMIN_PASSWORD", "obsolete")
	restarted, err := newServer(f.s, f.e, f.c, f.data, f.output)
	if err != nil {
		t.Fatal(err)
	}
	if w := authLogin(restarted.handler(), authNewPassword); w.Code != 200 {
		t.Fatal("password lost on restart", w.Code)
	}
	if f.s.signature("existing-media") != signature {
		t.Fatal("password changed playback signing key")
	}
	if s.eventToken != restarted.eventToken {
		t.Fatal("rotated ingest token did not survive restart")
	}
}

func TestCredentialPublicationReportsCommittedSyncFailure(t *testing.T) {
	dir := t.TempDir()
	want := &adminCredential{Version: 1, Iterations: 600000, Salt: make([]byte, 16), Hash: make([]byte, 32)}
	syncErr := errors.New("injected directory sync failure")
	err := writeAdminCredential(dir, want, func(*os.File) error { return syncErr })
	var published *adminCredentialPublishedError
	if !errors.As(err, &published) || !errors.Is(err, syncErr) {
		t.Fatal("publication failure did not report committed credential", err)
	}
	got, err := loadAdminCredential(dir)
	if err != nil || got == nil || got.Version != want.Version {
		t.Fatal("published credential could not be loaded", err)
	}
}

func TestPasswordChangeValidationAndRequestProtection(t *testing.T) {
	_, _, h, cookie := authFixture(t)
	valid := object{"current_password": authOldPassword, "new_password": authNewPassword, "confirm_password": authNewPassword}
	if w := generationPOST(h, &http.Cookie{Name: "unrelated", Value: "none"}, "/api/password", valid); w.Code != 401 {
		t.Fatal("unauthenticated password change accepted", w.Code)
	}
	body, _ := json.Marshal(valid)
	req := httptest.NewRequest("POST", "/api/password", strings.NewReader(string(body)))
	req.AddCookie(cookie)
	req.Header.Set("Origin", "http://untrusted.example")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 403 {
		t.Fatal("cross-origin password change accepted", w.Code)
	}
	for _, in := range []object{
		{"current_password": "wrong", "new_password": authNewPassword, "confirm_password": authNewPassword},
		{"current_password": authOldPassword, "new_password": "", "confirm_password": ""},
		{"current_password": authOldPassword, "new_password": authNewPassword, "confirm_password": "different-password"},
	} {
		if w := generationPOST(h, cookie, "/api/password", in); w.Code != 400 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if w := authLogin(h, authOldPassword); w.Code != 200 {
		t.Fatal("validation failure changed password")
	}
}

func TestPasswordAcceptsShortLongAndUnchanged(t *testing.T) {
	// Password changes require at least 10 characters (G1-3 fix): a
	// one-character new password must be rejected with 400 and leave the
	// current password in place, while long and unchanged passwords are
	// still accepted and survive a restart.
	for _, entry := range []struct {
		name, password string
		accepted       bool
	}{
		{"one character", "1", false},
		{"longer than prior limits", strings.Repeat("a", 2048), true},
		{"unchanged", authOldPassword, true},
	} {
		t.Run(entry.name, func(t *testing.T) {
			f, _, h, cookie := authFixture(t)
			w := generationPOST(h, cookie, "/api/password", object{"current_password": authOldPassword, "new_password": entry.password, "confirm_password": entry.password})
			if !entry.accepted {
				if w.Code != 400 {
					t.Fatal("short password was accepted", w.Code, w.Body.String())
				}
				if authLogin(h, authOldPassword).Code != 200 {
					t.Fatal("rejected change altered the password")
				}
				return
			}
			if w.Code != 200 || authLogin(h, entry.password).Code != 200 {
				t.Fatal("chosen password was rejected", w.Code)
			}
			restarted, err := newServer(f.s, f.e, f.c, f.data, f.output)
			if err != nil || authLogin(restarted.handler(), entry.password).Code != 200 {
				t.Fatal("chosen password failed after restart", err)
			}
		})
	}
}

func TestInitialPasswordAcceptsOneCharacter(t *testing.T) {
	f := setup(t)
	t.Setenv("STRMLINK_ADMIN_PASSWORD", "1")
	s, err := newServer(f.s, f.e, f.c, f.data, f.output)
	if err != nil || authLogin(s.handler(), "1").Code != 200 {
		t.Fatal("short bootstrap password rejected", err)
	}
}

func TestPasswordSaveFailurePreservesCredentialAndSession(t *testing.T) {
	f, _, h, cookie := authFixture(t)
	if err := os.Mkdir(filepath.Join(f.data, "admin-credential.json"), 0700); err != nil {
		t.Fatal(err)
	}
	w := generationPOST(h, cookie, "/api/password", object{"current_password": authOldPassword, "new_password": authNewPassword, "confirm_password": authNewPassword})
	if w.Code != 500 {
		t.Fatal(w.Code, w.Body.String())
	}
	req := httptest.NewRequest("GET", "/api/state", nil)
	req.AddCookie(cookie)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 || authLogin(h, authOldPassword).Code != 200 {
		t.Fatal("failed persistence locked administrator out")
	}
}

// generationPOSTFromAddr is generationPOST with an explicit RemoteAddr, so
// a test can place its requests in a rate-limit bucket of their own instead
// of silently sharing the bucket the fixture's login already counted into.
func generationPOSTFromAddr(h http.Handler, cookie *http.Cookie, path, remoteAddr string, body any) *httptest.ResponseRecorder {
	raw, _ := json.Marshal(body)
	r := httptest.NewRequest("POST", path, strings.NewReader(string(raw)))
	r.RemoteAddr = remoteAddr
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestPasswordChangeRateLimitAndCorruptStorage(t *testing.T) {
	f, _, h, cookie := authFixture(t)
	// Drive the limiter from a dedicated source address: the count below is
	// exactly the ten admitted attempts, independent of the fixture login
	// (which is counted in the default bucket, not this one).
	const addr = "198.51.100.23:4321"
	for i := 0; i < 10; i++ {
		if w := generationPOSTFromAddr(h, cookie, "/api/password", addr, object{"current_password": "wrong", "new_password": authNewPassword, "confirm_password": authNewPassword}); w.Code != 400 {
			t.Fatal(i, w.Code)
		}
	}
	if w := generationPOSTFromAddr(h, cookie, "/api/password", addr, object{"current_password": authOldPassword, "new_password": authNewPassword, "confirm_password": authNewPassword}); w.Code != 429 {
		t.Fatal("password attempts not bounded", w.Code)
	}
	os.WriteFile(filepath.Join(f.data, "admin-credential.json"), []byte(`{"version":1,"iterations":1}`), 0600)
	if _, err := newServer(f.s, f.e, f.c, f.data, f.output); err == nil {
		t.Fatal("corrupt persisted credential fell back to bootstrap password")
	}
}

func TestSessionExpiryAndLogoutInvalidateSession(t *testing.T) {
	_, s, h, cookie := authFixture(t)
	state := func(c *http.Cookie) int {
		req := httptest.NewRequest("GET", "/api/state", nil)
		req.AddCookie(c)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w.Code
	}
	if code := state(cookie); code != 200 {
		t.Fatal("fresh session rejected", code)
	}
	// Move this session's expiry into the past; the next request must be
	// rejected and the session dropped.
	s.mu.Lock()
	s.sessions[cookie.Value] = time.Now().Add(-time.Minute)
	s.mu.Unlock()
	if code := state(cookie); code != 401 {
		t.Fatal("expired session accepted", code)
	}
	fresh := authLogin(h, authOldPassword).Result().Cookies()[0]
	if code := state(fresh); code != 200 {
		t.Fatal("re-login rejected", code)
	}
	if w := generationPOST(h, fresh, "/api/logout", object{}); w.Code != 200 {
		t.Fatal("logout failed", w.Code, w.Body.String())
	}
	if code := state(fresh); code != 401 {
		t.Fatal("session survived logout", code)
	}
}

func TestLoginRateLimitAppliesToLoginEndpoint(t *testing.T) {
	f := setup(t)
	t.Setenv("STRMLINK_ADMIN_PASSWORD", authOldPassword)
	s, err := newServer(f.s, f.e, f.c, f.data, f.output)
	if err != nil {
		t.Fatal(err)
	}
	h := s.handler()
	// This Server instance has seen no prior attempts, so its bucket starts
	// empty and the counts are exact: ten failed logins are admitted, the
	// eleventh is limited before any password check happens.
	for i := 1; i <= 10; i++ {
		if w := authLogin(h, "wrong-password"); w.Code != 401 {
			t.Fatalf("failed login %d: got %d, want 401", i, w.Code)
		}
	}
	if w := authLogin(h, "wrong-password"); w.Code != 429 {
		t.Fatal("eleventh login attempt was not rate limited", w.Code)
	}
	if w := authLogin(h, authOldPassword); w.Code != 429 {
		t.Fatal("rate limit did not precede password verification", w.Code)
	}
}

func TestServePasswordReview(t *testing.T) {
	if os.Getenv("STRMLINK_PASSWORD_UI_REVIEW") != "1" {
		t.Skip("local UI fixture")
	}
	f := setup(t)
	f.a.Name = "移动媒体盘（测试账号）"
	f.s.saveAccount(f.a)
	t.Setenv("STRMLINK_ADMIN_PASSWORD", "StrmLink-Password-QA")
	s, err := newServer(f.s, f.e, f.c, f.data, f.output)
	if err != nil {
		t.Fatal(err)
	}
	runReviewFixture(t, s.handler(), "synthetic password form")
}
