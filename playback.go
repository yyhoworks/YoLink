package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

type cachedTarget struct {
	target  PlaybackTarget
	expires time.Time
}
type targetFlight struct {
	done   chan struct{}
	target PlaybackTarget
	err    error
}
type playbackCache struct {
	mu           sync.Mutex
	items        map[string]cachedTarget
	flights      map[string]*targetFlight
	hits, misses int64
	epoch        uint64
}

func newPlaybackCache() *playbackCache {
	return &playbackCache{items: map[string]cachedTarget{}, flights: map[string]*targetFlight{}}
}
func (c *playbackCache) clear() { c.mu.Lock(); clear(c.items); c.epoch++; c.mu.Unlock() }
func (c *playbackCache) stats() object {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for k, v := range c.items {
		if time.Now().Before(v.expires) {
			n++
		} else {
			delete(c.items, k)
		}
	}
	return object{"entries": n, "hits": c.hits, "misses": c.misses}
}
func targetExpiry(t PlaybackTarget) time.Time {
	now := time.Now()
	expiry := now.Add(45 * time.Second)
	u, e := url.Parse(t.URL)
	if e != nil {
		return now
	}
	for _, key := range []string{"expires", "Expires", "expire", "e"} {
		if n, e := strconv.ParseInt(u.Query().Get(key), 10, 64); e == nil {
			if n > 1e12 {
				n /= 1000
			}
			if n > 0 {
				candidate := time.Unix(n, 0).Add(-10 * time.Second)
				if candidate.Before(expiry) {
					expiry = candidate
				}
			}
		}
	}
	return expiry
}
func cloneTarget(t PlaybackTarget) PlaybackTarget { t.Headers = t.Headers.Clone(); return t }
func (s *Server) resolveMedia(ctx context.Context, m Media, force bool) (PlaybackTarget, bool, error) {
	started := time.Now()
	target, hit, err := s.resolveMediaTarget(ctx, m, force)
	v := s.mediaActivity(ctx, m)
	v.Kind = "direct"
	v.Mode = target.Mode
	v.Cache = hit
	v.Host = targetHost(target)
	v.Millis = time.Since(started).Milliseconds()
	v.Outcome = "resolved"
	if err != nil {
		v.Outcome = "failed"
		v.Error = err.Error()
	} else if target.Mode == "proxy-required" {
		v.Outcome = "proxy_required"
		v.Note = "该通道需要请求头，未允许中转"
	}
	s.s.appendActivity(v)
	return target, hit, err
}
func (s *Server) resolveMediaTarget(ctx context.Context, m Media, force bool) (PlaybackTarget, bool, error) {
	a, e := s.s.account(m.Account)
	if e != nil || a.Disabled {
		return PlaybackTarget{}, false, errors.New("媒体所属网盘不可用或已停用，请先启用")
	}
	raw, _ := json.Marshal(a)
	digest := sha256.Sum256(raw)
	key := m.ID + ":" + hex.EncodeToString(digest[:])
	if a.Provider == "115" {
		// 115 download links may be bound to the requesting player's User-Agent.
		ua := sha256.Sum256([]byte(download115UA(ctx)))
		key += ":" + hex.EncodeToString(ua[:])
	}
	c := s.links
	c.mu.Lock()
	epoch := c.epoch
	key += ":" + strconv.FormatUint(epoch, 10)
	if force {
		delete(c.items, key)
	}
	if v, ok := c.items[key]; ok && time.Now().Before(v.expires) {
		c.hits++
		c.mu.Unlock()
		return cloneTarget(v.target), true, nil
	}
	if f, ok := c.flights[key]; ok {
		c.hits++
		c.mu.Unlock()
		select {
		case <-ctx.Done():
			return PlaybackTarget{}, true, ctx.Err()
		case <-f.done:
			return cloneTarget(f.target), true, f.err
		}
	}
	f := &targetFlight{done: make(chan struct{})}
	c.flights[key] = f
	c.misses++
	c.mu.Unlock()
	go func() {
		bounded, cancel := context.WithTimeout(context.WithoutCancel(ctx), 45*time.Second)
		defer cancel()
		var target PlaybackTarget
		var err error
		if m.Legacy && m.RemoteID == "" {
			m, err = s.resolveLegacy(bounded, m)
		}
		if err == nil {
			target, err = s.clouds.Resolve(bounded, m)
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		f.target = cloneTarget(target)
		f.err = err
		if err == nil && target.Mode == "302" && epoch == c.epoch {
			if len(c.items) >= 2048 {
				for k := range c.items {
					delete(c.items, k)
					break
				}
			}
			c.items[key] = cachedTarget{cloneTarget(target), targetExpiry(target)}
		}
		delete(c.flights, key)
		close(f.done)
	}()
	select {
	case <-ctx.Done():
		return PlaybackTarget{}, false, ctx.Err()
	case <-f.done:
		return cloneTarget(f.target), false, f.err
	}
}

type PlaybackRecord struct {
	Info       Activity `json:"-"`
	Time       int64    `json:"time"`
	Media      string   `json:"media"`
	Name       string   `json:"name"`
	Action     string   `json:"action"`
	Mode       string   `json:"mode"`
	Cache      bool     `json:"cache"`
	Millis     int64    `json:"millis"`
	Status     int      `json:"status"`
	Readable   bool     `json:"readable"`
	Range      bool     `json:"range"`
	HeadStatus int      `json:"head_status"`
	Bytes      int64    `json:"bytes"`
	Error      string   `json:"error,omitempty"`
	Note       string   `json:"note,omitempty"`
}

func (s *Server) recordPlayback(v PlaybackRecord) {
	v.Time = time.Now().Unix()
	s.playMu.Lock()
	s.playHistory = append([]PlaybackRecord{v}, s.playHistory...)
	if len(s.playHistory) > 50 {
		s.playHistory = s.playHistory[:50]
	}
	s.playMu.Unlock()
	a := v.Info
	if a.Media == "" {
		if m, err := s.s.media(v.Media); err == nil {
			a = s.mediaActivity(context.Background(), m)
		}
	}
	a.Kind = "playback"
	a.Media = v.Media
	a.Name = v.Name
	a.Event = v.Action
	a.Mode = v.Mode
	a.Millis = v.Millis
	a.HTTP = v.Status
	a.Bytes = v.Bytes
	a.Cache = v.Cache
	a.Error = v.Error
	a.Note = v.Note
	switch {
	case v.Error != "" || v.Status >= 400:
		a.Outcome = "failed"
	case v.Action == "check":
		a.Kind = "direct"
		a.Outcome = "readable"
	case v.Action == "head":
		a.Outcome = "metadata"
	case v.Status == 302:
		a.Outcome = "redirected"
	case v.Mode == "proxy":
		a.Outcome = "proxied"
	default:
		a.Outcome = "handled"
	}
	if v.Action == "check" {
		a.Kind = "direct"
		if !v.Readable {
			a.Outcome = "failed"
		}
	}
	s.s.appendActivity(a)
}
func (s *Server) playbackState() object {
	s.playMu.Lock()
	history := append([]PlaybackRecord{}, s.playHistory...)
	s.playMu.Unlock()
	return object{"cache": s.links.stats(), "history": history}
}

func safeMediaClient() *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil
	tr.ResponseHeaderTimeout = 12 * time.Second
	tr.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, e := net.SplitHostPort(address)
		if e != nil {
			return nil, e
		}
		ips, e := net.DefaultResolver.LookupIPAddr(ctx, host)
		if e != nil {
			return nil, e
		}
		if len(ips) == 0 {
			return nil, errors.New("下载主机没有可用地址")
		}
		for _, v := range ips {
			ip := v.IP
			if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() || ip.IsMulticast() {
				return nil, errors.New("下载地址指向内网，已拒绝请求")
			}
		}
		var last error
		for _, v := range ips {
			conn, e := (&net.Dialer{Timeout: 8 * time.Second}).DialContext(ctx, network, net.JoinHostPort(v.IP.String(), port))
			if e == nil {
				return conn, nil
			}
			last = e
		}
		return nil, last
	}
	return &http.Client{Transport: tr, Timeout: 20 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 4 {
			return errors.New("下载地址跳转次数过多")
		}
		if req.URL.Scheme != "http" && req.URL.Scheme != "https" || req.URL.User != nil {
			return errors.New("下载地址无效")
		}
		if len(via) > 0 && !strings.EqualFold(via[0].URL.Hostname(), req.URL.Hostname()) {
			req.Header.Del("Cookie")
			req.Header.Del("Authorization")
		}
		return nil
	}}
}
func (s *Server) probeTarget(ctx context.Context, target PlaybackTarget) PlaybackRecord {
	result := PlaybackRecord{Mode: target.Mode}
	if target.Mode == "proxy-required" {
		result.Error = "当前通道需要服务器中转，请先在网盘设置允许中转"
		return result
	}
	client := s.probeClient
	if client == nil {
		client = safeMediaClient()
		defer client.CloseIdleConnections()
	}
	request := func(method string) (*http.Response, error) {
		req, e := http.NewRequestWithContext(ctx, method, target.URL, nil)
		if e != nil {
			return nil, e
		}
		req.Header = target.Headers.Clone()
		if req.Header == nil {
			req.Header = make(http.Header)
		}
		req.Header.Set("Accept-Encoding", "identity")
		if method == "GET" {
			req.Header.Set("Range", "bytes=0-1023")
		}
		return client.Do(req)
	}
	if head, e := request("HEAD"); e == nil {
		result.HeadStatus = head.StatusCode
		head.Body.Close()
	}
	resp, e := request("GET")
	if e != nil {
		result.Error = "无法读取媒体数据，请检查网盘权益、网络或直链有效期"
		return result
	}
	defer resp.Body.Close()
	result.Status = resp.StatusCode
	if resp.StatusCode != 200 && resp.StatusCode != 206 {
		result.Error = "媒体取流返回 HTTP " + strconv.Itoa(resp.StatusCode)
		return result
	}
	contentType := strings.ToLower(resp.Header.Get("Content-Type"))
	if strings.Contains(contentType, "text/html") || strings.Contains(contentType, "application/json") {
		result.Error = "下载地址返回了网页或接口数据，未取得媒体"
		return result
	}
	n, e := io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))
	result.Bytes = n
	result.Readable = n > 0 && e == nil
	endPart := strings.TrimPrefix(resp.Header.Get("Content-Range"), "bytes 0-")
	endText, _, rangeShape := strings.Cut(endPart, "/")
	end, rangeErr := strconv.ParseInt(endText, 10, 64)
	result.Range = result.Readable && resp.StatusCode == 206 && strings.HasPrefix(resp.Header.Get("Content-Range"), "bytes 0-") && rangeShape && rangeErr == nil && end >= 0 && end <= 1023 && n == end+1
	if !result.Readable {
		result.Error = "媒体响应没有可读取的数据"
	} else if !result.Range {
		result.Note = "已读取媒体数据，上游未确认 Range 支持，拖动仍需播放器验证"
	} else {
		result.Note = "已读取媒体数据并确认 Range 响应；音轨和实际拖动仍需播放器验证"
	}
	return result
}
func (s *Server) checkPlayback(w http.ResponseWriter, r *http.Request) {
	m, e := s.s.media(r.PathValue("id"))
	if e != nil {
		fail(w, 404, "媒体尚未生成")
		return
	}
	started := time.Now()
	ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
	defer cancel()
	target, hit, e := s.resolveMedia(ctx, m, false)
	result := PlaybackRecord{Media: m.ID, Name: m.Name, Action: "check", Cache: hit}
	if e != nil {
		result.Error = e.Error()
	} else {
		result = s.probeTarget(ctx, target)
		result.Media = m.ID
		result.Name = m.Name
		result.Action = "check"
		result.Cache = hit
		if result.Status == 401 || result.Status == 403 || result.Status == 410 {
			if fresh, _, er := s.resolveMedia(ctx, m, true); er == nil {
				result = s.probeTarget(ctx, fresh)
				result.Media = m.ID
				result.Name = m.Name
				result.Action = "check"
				result.Cache = false
			}
		}
	}
	result.Millis = time.Since(started).Milliseconds()
	result.Info = s.mediaActivity(r.Context(), m)
	s.recordPlayback(result)
	jsonOut(w, 200, result)
}
