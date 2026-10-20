package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

const fixture115Cookie = "UID=115-fixture; CID=fixture-cid; SEID=fixture-seid; KID=fixture-kid"

type transport115Func func(*http.Request) (*http.Response, error)

func (fn transport115Func) RoundTrip(r *http.Request) (*http.Response, error) { return fn(r) }
func response115(r *http.Request, value any) *http.Response {
	b, _ := json.Marshal(value)
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(bytes.NewReader(b)), Request: r}
}
func fixture115(t *testing.T, f *fixture, fn transport115Func) *Providers {
	t.Helper()
	a := Account{ID: "115-account", Name: "115 合成账号", Provider: "115", Root: "0", Secrets: map[string]string{"cookie": fixture115Cookie}}
	if err := f.s.saveAccount(a); err != nil {
		t.Fatal(err)
	}
	p := newProviders(f.s)
	p.client.Transport = fn
	return p
}
func Test115CookieImportValidationAndRedaction(t *testing.T) {
	f := setup(t)
	h, cookie := generationHandler(t, f)
	for _, value := range []string{"", "UID=1; CID=2", "UID=1; CID=2; SEID=3\nmalformed"} {
		w := generationPOST(h, cookie, "/api/accounts", Account{Name: "115 test", Provider: "115", Secrets: map[string]string{"cookie": value}})
		if w.Code != 400 || strings.Contains(w.Body.String(), "malformed") {
			t.Fatal("invalid Cookie accepted or echoed", w.Code)
		}
	}
	w := generationPOST(h, cookie, "/api/accounts", Account{Name: "115 test", Provider: "115", Secrets: map[string]string{"cookie": "Cookie: " + fixture115Cookie}})
	var a Account
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &a) != nil || a.Root != "0" || a.Secrets != nil || strings.Contains(w.Body.String(), "fixture-seid") {
		t.Fatal("account import metadata or redaction failed", w.Code)
	}
	saved, err := f.s.account(a.ID)
	if err != nil || saved.Secrets["cookie"] != fixture115Cookie {
		t.Fatal("Cookie not persisted")
	}
	var sealed []byte
	f.s.db.QueryRow("SELECT secret FROM accounts WHERE id=?", a.ID).Scan(&sealed)
	if bytes.Contains(sealed, []byte("fixture-seid")) {
		t.Fatal("Cookie was stored as plaintext")
	}
	w = generationPOST(h, cookie, "/api/accounts", Account{ID: a.ID, Name: "Renamed", Provider: "115"})
	if w.Code != 200 {
		t.Fatal("editing with empty credentials must preserve them", w.Code)
	}
	w = generationPOST(h, cookie, "/api/accounts", Account{Name: "Invalid root", Provider: "115", Root: "root", Secrets: saved.Secrets})
	if w.Code != 400 {
		t.Fatal("nonnumeric root accepted")
	}
}
func Test115ListingPaginationQuotaAndSTRMPickcode(t *testing.T) {
	f := setup(t)
	p := fixture115(t, f, func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Cookie") != fixture115Cookie || r.URL.Scheme != "https" || r.Method != "GET" {
			t.Fatal("unexpected credentials or request")
		}
		if r.URL.Path == "/files/index_info" {
			return response115(r, object{"state": true, "data": object{"space_info": object{"all_total": object{"size": "1000000"}, "all_use": object{"size": 125000}}}}), nil
		}
		q := r.URL.Query()
		var rows []object
		count := 3
		if q.Get("cid") == "2" {
			count = 1
			rows = []object{{"fid": "12", "cid": "2", "n": "S01E01.mp4", "s": "2048", "pc": "pick-episode"}}
		} else if q.Get("cid") == "0" && q.Get("offset") == "0" {
			rows = []object{{"cid": "2", "pid": "0", "n": "剧集"}, {"fid": "9007199254740993", "cid": "0", "n": "电影 &amp; 测试.mkv", "s": 4096, "pc": "pick-movie"}}
		} else if q.Get("cid") == "0" && q.Get("offset") == "2" {
			rows = []object{{"fid": "13", "cid": "0", "n": "audio.flac", "s": "1024", "pc": "pick-audio"}}
		} else {
			t.Fatal("incorrect pagination or directory", q.Get("cid"), q.Get("offset"))
		}
		offset, _ := strconv.Atoi(q.Get("offset"))
		return response115(r, object{"state": true, "cid": q.Get("cid"), "count": count, "offset": offset, "data": rows}), nil
	})
	entries, err := p.List(context.Background(), "115-account", "0")
	if err != nil || len(entries) != 3 || !entries[0].Dir || entries[1].ID != "9007199254740993" || entries[1].Name != "电影 & 测试.mkv" || entries[1].PickCode != "pick-movie" {
		t.Fatal("incorrect directory mapping", err, entries)
	}
	quota, err := p.Quota(context.Background(), "115-account")
	if err != nil || quota.Total != 1000000 || quota.Used != 125000 {
		t.Fatal("incorrect quota", err, quota)
	}
	f.e.clouds = p
	f.r.Account, f.r.Folder = "115-account", "0"
	if err = f.s.saveRule(f.r); err != nil {
		t.Fatal(err)
	}
	j := generate(t, f, false, 0)
	if j.Status != "completed" || j.CreatedFiles != 3 {
		t.Fatal("115 recursive STRM generation failed", j)
	}
	a, _ := f.s.account("115-account")
	m, err := f.s.media(f.s.mediaID(a, "9007199254740993"))
	if err != nil || m.PickCode != "pick-movie" || m.RemoteID != "9007199254740993" {
		t.Fatal("generation lost stable file ID or pickcode", err)
	}
}

// Emulate the provider side of the wire protocol with an isolated RSA key.
func reply115(t *testing.T, private *rsa.PrivateKey, r *http.Request, value any) string {
	t.Helper()
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") || r.ParseForm() != nil {
		t.Fatal("download API must receive a form")
	}
	ciphertext, err := base64.StdEncoding.DecodeString(r.Form.Get("data"))
	if err != nil || len(ciphertext) != private.Size() {
		t.Fatal("invalid encrypted request")
	}
	packet, err := rsa.DecryptPKCS1v15(rand.Reader, private, ciphertext)
	if err != nil || len(packet) <= 16 {
		t.Fatal("request cannot be decoded")
	}
	key := packet[:16]
	xor115(packet[16:], cipher115ClientKey)
	reverse115(packet[16:])
	xor115(packet[16:], derive115Key(key, 4))
	var request map[string]string
	if json.Unmarshal(packet[16:], &request) != nil || request["pickcode"] != "pick-movie" {
		t.Fatal("request lost pickcode")
	}
	plain, _ := json.Marshal(value)
	serverKey := bytes.Repeat([]byte{0x42}, 16)
	xor115(plain, derive115Key(key, 4))
	reverse115(plain)
	xor115(plain, derive115Key(serverKey, 12))
	plain = append(serverKey, plain...)
	var result []byte
	for len(plain) > 0 {
		n := min(len(plain), private.Size()-11)
		block := bytes.Repeat([]byte{0xff}, private.Size())
		block[0], block[1] = 0, 1
		separator := len(block) - n - 1
		block[separator] = 0
		copy(block[separator+1:], plain[:n])
		signed := new(big.Int).Exp(new(big.Int).SetBytes(block), private.D, private.N)
		result = append(result, signed.FillBytes(make([]byte, private.Size()))...)
		plain = plain[n:]
	}
	return base64.StdEncoding.EncodeToString(result)
}
func Test115Download302UsesPlayerUAAndIsolatesCache(t *testing.T) {
	private, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	old := cipher115Public
	cipher115Public = &private.PublicKey
	t.Cleanup(func() { cipher115Public = old })
	f := setup(t)
	calls, metadata := 0, 0
	p := fixture115(t, f, func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/files/get_info" {
			metadata++
			if r.URL.Query().Get("file_id") != "101" {
				t.Fatal("incorrect metadata lookup")
			}
			return response115(r, object{"state": true, "data": []object{{"fid": "101", "pc": "pick-movie"}}}), nil
		}
		if r.URL.Host != "proapi.115.com" || r.URL.Path != "/app/chrome/downurl" || r.Method != "POST" || r.Header.Get("Cookie") != fixture115Cookie {
			t.Fatal("unexpected download endpoint or missing credentials")
		}
		calls++
		encoded := reply115(t, private, r, object{"101": object{"file_name": "movie.mkv", "url": object{"url": "https://cdn.115.com/movie.mkv?client=" + r.UserAgent()}}})
		resp := response115(r, object{"state": true, "data": encoded})
		resp.Header.Add("Set-Cookie", "download_token=fixture-download; Path=/; HttpOnly")
		return resp, nil
	})
	s, h, _ := testToolsServer(t, f, p)
	m := Media{ID: "115-media", Account: "115-account", RemoteID: "101", PickCode: "pick-movie", Name: "movie.mkv"}
	f.s.saveMedia(m)
	ctxA := context.WithValue(context.Background(), pan115UserAgentKey{}, "PlayerA")
	ctxB := context.WithValue(context.Background(), pan115UserAgentKey{}, "PlayerB")
	target, hit, err := s.resolveMediaTarget(ctxA, m, false)
	if err != nil || hit || target.Mode != "302" || target.Headers.Get("User-Agent") != "PlayerA" || target.Headers.Get("Cookie") != "" || calls != 1 || metadata != 0 {
		t.Fatal("115 direct playback failed or leaked Cookie", err)
	}
	_, hit, err = s.resolveMediaTarget(ctxA, m, false)
	if err != nil || !hit || calls != 1 {
		t.Fatal("same player did not reuse link")
	}
	target, hit, err = s.resolveMediaTarget(ctxB, m, false)
	if err != nil || hit || calls != 2 || !strings.Contains(target.URL, "PlayerB") {
		t.Fatal("different players shared a UA-bound link")
	}
	r := httptest.NewRequest("GET", "/s/"+m.ID+"/movie.mkv?k="+f.s.signature(m.ID), nil)
	r.Header.Set("User-Agent", "PlayerA")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 302 || !strings.Contains(w.Header().Get("Location"), "PlayerA") || w.Header().Get("Set-Cookie") != "" || calls != 2 {
		t.Fatal("signed playback did not preserve the player UA or leaked credentials", w.Code)
	}
	a, _ := f.s.account(m.Account)
	a.AllowProxy = true
	f.s.saveAccount(a)
	target, hit, err = s.resolveMediaTarget(ctxA, m, false)
	if err != nil || hit || target.Mode != "proxy" || !strings.Contains(target.Headers.Get("Cookie"), "download_token=fixture-download") || !strings.Contains(target.Headers.Get("Cookie"), "SEID=fixture-seid") {
		t.Fatal("explicit proxy mode lost its required headers", err)
	}
	a, _ = f.s.account(m.Account)
	if a.Secrets["cookie"] != fixture115Cookie {
		t.Fatal("download cookie polluted saved login credentials")
	}
	m.PickCode = ""
	if _, err = p.Resolve(ctxA, m); err != nil || metadata != 1 {
		t.Fatal("old indexes cannot recover pickcode", err)
	}
}
func Test115StopsOnRejectedAndIncompleteResponses(t *testing.T) {
	f := setup(t)
	for _, value := range []object{
		{"state": false, "errno": 99, "error": "do-not-echo-fixture-secret"},
		{"state": true, "count": 1, "data": []any{}},
		{"state": true, "cid": "another-folder", "count": 1, "data": []any{}},
		{"state": true, "count": 0},
	} {
		p := fixture115(t, f, func(r *http.Request) (*http.Response, error) { return response115(r, value), nil })
		if _, err := p.List(context.Background(), "115-account", "0"); err == nil || strings.Contains(err.Error(), "do-not-echo") {
			t.Fatal("bad directory accepted or upstream error echoed")
		}
	}
	for _, input := range []string{"", "!", "AA==", base64.StdEncoding.EncodeToString(make([]byte, 128))} {
		if _, err := decode115(input, [16]byte{}); err == nil {
			t.Fatal("malformed encrypted response accepted")
		}
	}
	p := fixture115(t, f, func(r *http.Request) (*http.Response, error) {
		t.Fatal("cancelled request reached network")
		return nil, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.List(ctx, "115-account", "0"); err != context.Canceled {
		t.Fatal("cancel not propagated", err)
	}
}
