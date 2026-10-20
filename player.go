package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
)

// Cloud download signatures can be valid for GET but rejected for HEAD.
// Answer the player's preflight with bounded GET metadata; playback GET stays 302.
func (s *Server) playbackHead(w http.ResponseWriter, r *http.Request, target PlaybackTarget, fallbackSize int64) int {
	client := s.probeClient
	if client == nil {
		client = safeMediaClient()
		defer client.CloseIdleConnections()
	}
	req, err := http.NewRequestWithContext(r.Context(), "GET", target.URL, nil)
	if err != nil {
		fail(w, 502, "无法检查原文件信息")
		return 502
	}
	req.Header = target.Headers.Clone()
	if req.Header == nil {
		req.Header = make(http.Header)
	}
	req.Header.Set("Range", "bytes=0-0")
	req.Header.Set("Accept-Encoding", "identity")
	resp, err := client.Do(req)
	if err != nil {
		fail(w, 502, "原文件信息读取失败，请运行取流自检")
		return 502
	}
	defer resp.Body.Close()
	kind := strings.ToLower(resp.Header.Get("Content-Type"))
	if (resp.StatusCode != 200 && resp.StatusCode != 206) || strings.Contains(kind, "text/html") || strings.Contains(kind, "application/json") || strings.Contains(kind, "application/xml") {
		fail(w, 502, "网盘未返回可读取的媒体信息")
		return 502
	}
	size := fallbackSize
	if resp.StatusCode == 206 {
		prefix, total, ok := strings.Cut(resp.Header.Get("Content-Range"), "/")
		n, parseErr := strconv.ParseInt(total, 10, 64)
		if !ok || prefix != "bytes 0-0" || parseErr != nil || n <= 0 {
			fail(w, 502, "网盘返回的媒体范围信息无效")
			return 502
		}
		size = n
	} else if resp.ContentLength > 0 {
		size = resp.ContentLength
	}
	if n, er := io.Copy(io.Discard, io.LimitReader(resp.Body, 1)); er != nil || n != 1 {
		fail(w, 502, "原文件为空或无法读取")
		return 502
	}
	for _, h := range []string{"Content-Type", "ETag", "Last-Modified"} {
		if v := resp.Header.Get(h); v != "" {
			w.Header().Set(h, v)
		}
	}
	if size > 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	}
	if resp.StatusCode == 206 || resp.Header.Get("Accept-Ranges") == "bytes" {
		w.Header().Set("Accept-Ranges", "bytes")
	}
	w.WriteHeader(200)
	return 200
}

func (s *Server) mediaPlaylist(w http.ResponseWriter, r *http.Request) {
	m, err := s.s.media(r.PathValue("id"))
	if err != nil {
		fail(w, 404, "媒体索引不存在")
		return
	}
	a, err := s.s.account(m.Account)
	if err != nil || a.Disabled {
		fail(w, 409, "媒体所属网盘不可用或已停用")
		return
	}
	s.writePlaylist(w, strings.TrimSuffix(m.Name, path.Ext(m.Name)), []Media{m})
}

func (s *Server) rulePlaylist(w http.ResponseWriter, r *http.Request) {
	rule, err := s.s.rule(r.PathValue("id"))
	if err != nil {
		fail(w, 404, "目录规则不存在")
		return
	}
	a, err := s.s.account(rule.Account)
	if err != nil || a.Disabled {
		fail(w, 409, "目录所属网盘不可用或已停用")
		return
	}
	_, _, output, err := s.engine.outputLocation(rule.Output)
	if err != nil {
		fail(w, 400, "目录规则的输出映射不可用")
		return
	}
	output = strings.ReplaceAll(output, `\`, "/")
	rows, err := s.s.db.QueryContext(r.Context(), `SELECT m.payload,o.path FROM media m JOIN outputs o ON o.media=m.id WHERE m.account=? AND lower(o.path) LIKE '%.strm' ORDER BY m.source_path,m.id`, rule.Account)
	if err != nil {
		fail(w, 500, "播放列表读取失败")
		return
	}
	defer rows.Close()
	media, seen := []Media{}, map[string]bool{}
	for rows.Next() {
		var raw, dest string
		var m Media
		if rows.Scan(&raw, &dest) != nil || json.Unmarshal([]byte(raw), &m) != nil {
			fail(w, 500, "媒体索引读取失败")
			return
		}
		dest = strings.ReplaceAll(dest, `\`, "/")
		if !strings.HasPrefix(m.Path, strings.TrimSuffix(rule.SourcePath, "/")+"/") || output != "." && !strings.HasPrefix(dest, strings.TrimSuffix(output, "/")+"/") || seen[m.ID] {
			continue
		}
		media = append(media, m)
		seen[m.ID] = true
		if len(media) > 100000 {
			fail(w, 400, "播放列表超过 100000 个媒体，请缩小目录规则范围")
			return
		}
	}
	if rows.Err() != nil {
		fail(w, 500, "播放列表读取失败")
		return
	}
	s.writePlaylist(w, rule.Name, media)
}

func (s *Server) writePlaylist(w http.ResponseWriter, name string, media []Media) {
	base, err := validBase(s.s.setting("public_base", ""))
	if err != nil {
		fail(w, 400, "请先设置播放器可访问的服务地址")
		return
	}
	if len(media) == 0 {
		fail(w, 404, "这个目录还没有生成媒体，请先生成 STRM")
		return
	}
	sort.SliceStable(media, func(i, j int) bool { return media[i].Path < media[j].Path })
	var body strings.Builder
	body.WriteString("#EXTM3U\n")
	for _, m := range media {
		title := strings.Map(func(c rune) rune {
			if c < 32 {
				return ' '
			}
			return c
		}, m.Name)
		body.WriteString("#EXTINF:-1," + title + "\n")
		body.WriteString(mediaContent(s.s, m, base))
	}
	filename, err := safeName(name)
	if err != nil {
		filename = "YoLink"
	}
	w.Header().Set("Content-Type", "audio/x-mpegurl; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="YoLink-playlist.m3u"; filename*=UTF-8''`+url.PathEscape(filename+".m3u"))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	io.WriteString(w, body.String())
}
