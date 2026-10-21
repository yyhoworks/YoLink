package main

import (
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"time"
)

type adminCredential struct {
	Version    int    `json:"version"`
	Iterations int    `json:"iterations"`
	Salt       []byte `json:"salt"`
	Hash       []byte `json:"hash"`
}

// Publication has already replaced the credential even if the following
// directory sync fails. Callers updating live authentication must adopt it.
type adminCredentialPublishedError struct{ err error }

func (e *adminCredentialPublishedError) Error() string { return e.err.Error() }
func (e *adminCredentialPublishedError) Unwrap() error { return e.err }

func loadAdminCredential(data string) (*adminCredential, error) {
	raw, err := os.ReadFile(filepath.Join(data, "admin-credential.json"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, errors.New("管理员密码配置无法读取")
	}
	var c adminCredential
	if json.Unmarshal(raw, &c) != nil || c.Version != 1 || c.Iterations != 600000 || len(c.Salt) != 16 || len(c.Hash) != 32 {
		return nil, errors.New("管理员密码配置损坏，请从备份恢复")
	}
	return &c, nil
}

func saveAdminCredential(data string, c *adminCredential) error {
	return writeAdminCredential(data, c, temporaryDirectorySync)
}

func writeAdminCredential(data string, c *adminCredential, syncDirectory func(*os.File) error) error {
	raw, err := json.Marshal(c)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(data, ".admin-"+randomID()+".tmp"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, err = f.Write(raw)
	if err == nil {
		err = f.Sync()
	}
	closed := f.Close()
	if err != nil {
		return err
	}
	if closed != nil {
		return closed
	}
	if err := os.Rename(f.Name(), filepath.Join(data, "admin-credential.json")); err != nil {
		return err
	}
	// Sync the parent directory so the rename itself survives a power loss.
	d, err := os.Open(data)
	if err != nil {
		return &adminCredentialPublishedError{err}
	}
	defer d.Close()
	if err := syncDirectory(d); err != nil {
		return &adminCredentialPublishedError{err}
	}
	return nil
}

// Caller holds authMu until any login session is issued or revoked.
func (s *Server) passwordMatches(password string) bool {
	if s.adminCredential == nil {
		hash := sha256.Sum256([]byte(password))
		return hmac.Equal(hash[:], s.adminHash[:])
	}
	c := s.adminCredential
	hash, err := pbkdf2.Key(sha256.New, password, c.Salt, c.Iterations, 32)
	return err == nil && hmac.Equal(hash, c.Hash)
}

// authAttemptKey aggregates IPv6 sources by their /64 prefix so one attacker
// cannot mint fresh counters per address; IPv4 and non-IP hosts are unchanged.
func authAttemptKey(host string) string {
	ip := net.ParseIP(host)
	if ip == nil || ip.To4() != nil {
		return host
	}
	return ip.Mask(net.CIDRMask(64, 128)).String()
}

// pruneAttemptsLocked first drops timestamps outside the one-minute window
// (and keys left empty), then, only if the table is still oversized, evicts
// the keys whose latest attempt is oldest. It never resets live counters the
// way a wholesale clear did. Caller holds s.mu.
func (s *Server) pruneAttemptsLocked(now time.Time) {
	for k, list := range s.attempts {
		kept := make([]time.Time, 0, len(list))
		for _, t := range list {
			if now.Sub(t) < time.Minute {
				kept = append(kept, t)
			}
		}
		if len(kept) == 0 {
			delete(s.attempts, k)
		} else {
			s.attempts[k] = kept
		}
	}
	if len(s.attempts) <= 4096 {
		return
	}
	type entry struct {
		key  string
		last time.Time
	}
	entries := make([]entry, 0, len(s.attempts))
	for k, list := range s.attempts {
		entries = append(entries, entry{k, list[len(list)-1]})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].last.Before(entries[j].last) })
	for _, e := range entries[:len(entries)-4096] {
		delete(s.attempts, e.key)
	}
}

func (s *Server) allowAuthAttempt(r *http.Request) bool {
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	if host == "" {
		host = r.RemoteAddr
	}
	key := authAttemptKey(host)
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	recent := []time.Time{}
	for _, t := range s.attempts[key] {
		if now.Sub(t) < time.Minute {
			recent = append(recent, t)
		}
	}
	if len(recent) >= 10 {
		return false
	}
	s.pruneAttemptsLocked(now)
	s.attempts[key] = append(recent, now)
	return true
}

func (s *Server) changePassword(w http.ResponseWriter, r *http.Request) {
	if !s.allowAuthAttempt(r) {
		fail(w, 429, "尝试次数过多，请一分钟后重试")
		return
	}
	var in struct {
		Current string `json:"current_password"`
		New     string `json:"new_password"`
		Confirm string `json:"confirm_password"`
	}
	if err := decode(w, r, &in); err != nil {
		fail(w, 400, err.Error())
		return
	}
	s.authMu.Lock()
	defer s.authMu.Unlock()
	if !s.passwordMatches(in.Current) {
		fail(w, 400, "当前密码不正确")
		return
	}
	if in.New == "" {
		fail(w, 400, "新密码不能为空")
		return
	}
	if len([]rune(in.New)) < 10 {
		fail(w, 400, "新密码至少需要 10 个字符")
		return
	}
	if in.New != in.Confirm {
		fail(w, 400, "两次输入的新密码不一致")
		return
	}
	c := &adminCredential{Version: 1, Iterations: 600000, Salt: make([]byte, 16)}
	if _, err := rand.Read(c.Salt); err != nil {
		fail(w, 500, "密码更新失败，请重试")
		return
	}
	var err error
	c.Hash, err = pbkdf2.Key(sha256.New, in.New, c.Salt, c.Iterations, 32)
	if err != nil {
		fail(w, 500, "密码更新失败，请重试")
		return
	}
	saveErr := saveAdminCredential(s.s.dir, c)
	var published *adminCredentialPublishedError
	if saveErr != nil && !errors.As(saveErr, &published) {
		fail(w, 500, "密码保存失败，请检查数据目录权限后重试")
		return
	}
	s.adminCredential = c
	s.mu.Lock()
	clear(s.sessions)
	s.mu.Unlock()
	// A password change is the recovery path for a compromised credential set,
	// so the legacy event token is rotated along with it; the old token stops
	// working immediately.
	rotateErr := s.rotateEventToken()
	http.SetCookie(w, &http.Cookie{Name: "strmlink_session", Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode})
	if rotateErr != nil {
		fail(w, 500, "密码已更新，但事件令牌轮换失败，请稍后在接入设置中手动轮换")
		return
	}
	if saveErr != nil {
		fail(w, 500, "密码已更新，请使用新密码重新登录；目录持久化确认失败，请检查数据磁盘。事件令牌已轮换，请更新接入方凭据")
		return
	}
	jsonOut(w, 200, object{"ok": true})
}
