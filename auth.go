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
	"time"
)

type adminCredential struct {
	Version    int    `json:"version"`
	Iterations int    `json:"iterations"`
	Salt       []byte `json:"salt"`
	Hash       []byte `json:"hash"`
}

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
	return os.Rename(f.Name(), filepath.Join(data, "admin-credential.json"))
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

func (s *Server) allowAuthAttempt(r *http.Request) bool {
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	if host == "" {
		host = r.RemoteAddr
	}
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	recent := []time.Time{}
	for _, t := range s.attempts[host] {
		if now.Sub(t) < time.Minute {
			recent = append(recent, t)
		}
	}
	if len(recent) >= 10 {
		return false
	}
	if len(s.attempts) > 4096 {
		clear(s.attempts)
	}
	s.attempts[host] = append(recent, now)
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
	if err != nil || saveAdminCredential(s.s.dir, c) != nil {
		fail(w, 500, "密码保存失败，请检查数据目录权限后重试")
		return
	}
	s.adminCredential = c
	s.mu.Lock()
	clear(s.sessions)
	s.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: "strmlink_session", Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode})
	jsonOut(w, 200, object{"ok": true})
}
