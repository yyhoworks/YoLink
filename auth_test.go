package main

import (
	"encoding/json"
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
	w := generationPOST(h, cookie, "/api/password", object{"current_password": authOldPassword, "new_password": authNewPassword, "confirm_password": authNewPassword})
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
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
		t.Fatal("password changed ingest token")
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
	for _, entry := range []struct{ name, password string }{{"one character", "1"}, {"longer than prior limits", strings.Repeat("a", 2048)}, {"unchanged", authOldPassword}} {
		t.Run(entry.name, func(t *testing.T) {
			f, _, h, cookie := authFixture(t)
			w := generationPOST(h, cookie, "/api/password", object{"current_password": authOldPassword, "new_password": entry.password, "confirm_password": entry.password})
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

func TestPasswordChangeRateLimitAndCorruptStorage(t *testing.T) {
	f, _, h, cookie := authFixture(t)
	for i := 0; i < 9; i++ {
		if w := generationPOST(h, cookie, "/api/password", object{"current_password": "wrong", "new_password": authNewPassword, "confirm_password": authNewPassword}); w.Code != 400 {
			t.Fatal(i, w.Code)
		}
	}
	if w := generationPOST(h, cookie, "/api/password", object{"current_password": authOldPassword, "new_password": authNewPassword, "confirm_password": authNewPassword}); w.Code != 429 {
		t.Fatal("password attempts not bounded", w.Code)
	}
	os.WriteFile(filepath.Join(f.data, "admin-credential.json"), []byte(`{"version":1,"iterations":1}`), 0600)
	if _, err := newServer(f.s, f.e, f.c, f.data, f.output); err == nil {
		t.Fatal("corrupt persisted credential fell back to bootstrap password")
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
	server := &http.Server{Addr: "127.0.0.1:18796", Handler: s.handler(), ReadHeaderTimeout: 5 * time.Second}
	t.Cleanup(func() { server.Close() })
	t.Log("Synthetic password form fixture http://127.0.0.1:18796")
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		t.Fatal(err)
	}
}
