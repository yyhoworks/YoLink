package main

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWallpaperCSPOnlyAllowsExternalHTTPSImages(t *testing.T) {
	f := setup(t)
	h, _ := generationHandler(t, f)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	policy := w.Header().Get("Content-Security-Policy")
	for _, directive := range []string{"img-src 'self' data: https:;", "script-src 'self';", "style-src 'self';", "connect-src 'self';"} {
		if !strings.Contains(policy, directive) {
			t.Fatalf("missing security directive %q in %s", directive, policy)
		}
	}
	if strings.Contains(policy, "http:") || strings.Contains(policy, "unsafe-inline") {
		t.Fatal("wallpaper weakened non-image protections", policy)
	}
	if w.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatal("wallpaper requests must not disclose the admin URL")
	}
}
