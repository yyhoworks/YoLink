package main

import (
	"errors"
	"net/http"
	"os"
	"strings"
	"time"
)

// Restore only a missing, registered STRM whose exact bytes can be verified.
// publish confines all filesystem operations to an output mount and never
// replaces an existing destination, including one created during this request.
func (s *Server) restoreMissingOutput(key string) (bool, error) {
	var mid, expected string
	if !strings.HasSuffix(strings.ToLower(key), ".strm") || s.s.db.QueryRow("SELECT media,hash FROM outputs WHERE path=?", key).Scan(&mid, &expected) != nil {
		return false, errors.New("只能修复本服务登记的 STRM")
	}
	root, rel, _, err := s.engine.outputLocation(key)
	if err != nil {
		return false, err
	}
	f, err := root.Open(rel)
	if err == nil {
		f.Close()
		return false, nil
	}
	if !os.IsNotExist(err) {
		return false, errors.New("文件不可访问，请检查映射与权限")
	}
	m, err := s.s.media(mid)
	if err != nil {
		return false, errors.New("媒体索引缺失，无法恢复")
	}
	base := s.s.setting("public_base", "")
	if base == "" {
		return false, errors.New("请先配置播放入口")
	}
	content := []byte(mediaContent(s.s, m, base))
	if contentHash(content) != expected {
		return false, errors.New("当前播放入口与登记内容不同，请先核对服务地址")
	}
	return s.engine.publish(key, mid, content)
}

func (s *Server) repairOutputs(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Paths []string `json:"paths"`
	}
	if err := decode(w, r, &in); err != nil {
		fail(w, 400, err.Error())
		return
	}
	if len(in.Paths) == 0 || len(in.Paths) > 50 {
		fail(w, 400, "每次请选择 1 到 50 个文件")
		return
	}
	s.maintenanceMu.Lock()
	defer s.maintenanceMu.Unlock()
	s.engine.mu.Lock()
	defer s.engine.mu.Unlock()
	var active int
	if err := s.s.db.QueryRow("SELECT COUNT(*) FROM jobs WHERE status IN ('running','queued')").Scan(&active); err != nil || active > 0 {
		fail(w, 409, "请等待生成任务结束后再修复缺失文件")
		return
	}
	items := []object{}
	seen := map[string]bool{}
	restored, preserved, failed := 0, 0, 0
	for _, key := range in.Paths {
		if seen[key] {
			continue
		}
		seen[key] = true
		if r.Context().Err() != nil {
			break
		}
		created, err := s.restoreMissingOutput(key)
		if err != nil {
			failed++
			items = append(items, object{"path": key, "status": "failed", "note": err.Error()})
			continue
		}
		if created {
			restored++
		} else {
			preserved++
		}
		status, note := s.inspectOutput(r.Context(), key, false)
		_, err = s.s.db.Exec("INSERT INTO output_details(path,rule,job,created,checked,status,note) VALUES(?,'','',0,?,?,?) ON CONFLICT(path) DO UPDATE SET checked=excluded.checked,status=excluded.status,note=excluded.note", key, time.Now().UnixMilli(), status, note)
		if err != nil {
			failed++
			note = "文件处理完成，体检结果保存失败，请重新体检"
		}
		items = append(items, object{"path": key, "status": status, "note": note, "restored": created})
	}
	jsonOut(w, 200, object{"items": items, "restored": restored, "preserved": preserved, "failed": failed})
}
