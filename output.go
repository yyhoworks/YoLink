package main

import (
	"bufio"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type outputMount struct {
	Path    string `json:"path"`
	Default bool   `json:"default"`
	root    *os.Root
}

func absoluteOutputPath(e *Engine, value string) string {
	root, rel, _, err := e.outputLocation(value)
	if err != nil {
		return value
	}
	return filepath.ToSlash(filepath.Join(root.Name(), rel))
}

func withinPath(value, base string) bool {
	rel, err := filepath.Rel(base, value)
	return err == nil && (rel == "." || filepath.IsLocal(rel))
}

func (e *Engine) addOutputMount(dir string, primary bool) error {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	abs = filepath.Clean(abs)
	for _, m := range e.outputMounts {
		if m.Path == filepath.ToSlash(abs) {
			return nil
		}
	}
	r, err := os.OpenRoot(abs)
	if err != nil {
		return err
	}
	e.outputMounts = append(e.outputMounts, outputMount{Path: filepath.ToSlash(abs), Default: primary, root: r})
	return nil
}

// Read the container's mount table; no Docker socket or host filesystem access.
func mappedOutputPaths(raw, data string) []string {
	paths := []string{}
	decode := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)
	scan := bufio.NewScanner(strings.NewReader(raw))
	for scan.Scan() {
		parts := strings.SplitN(scan.Text(), " - ", 2)
		if len(parts) != 2 {
			continue
		}
		left, right := strings.Fields(parts[0]), strings.Fields(parts[1])
		if len(left) < 6 || len(right) < 3 || !strings.Contains(","+left[5]+",", ",rw,") || !strings.Contains(","+right[2]+",", ",rw,") {
			continue
		}
		switch right[0] {
		case "proc", "sysfs", "tmpfs", "devtmpfs", "devpts", "cgroup", "cgroup2", "overlay", "mqueue", "securityfs", "debugfs", "tracefs":
			continue
		}
		p := filepath.Clean(decode.Replace(left[4]))
		if p == "/" || p == "/data" || withinPath(p, data) || withinPath(data, p) {
			continue
		}
		blocked := false
		for _, sys := range []string{"/proc", "/sys", "/dev", "/etc", "/tmp", "/run", "/usr", "/bin", "/sbin", "/lib", "/var"} {
			if withinPath(p, sys) {
				blocked = true
				break
			}
		}
		if !blocked {
			paths = append(paths, p)
		}
	}
	return paths
}

func (e *Engine) discoverOutputMounts() {
	raw, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return
	}
	data, _ := filepath.Abs(e.s.dir)
	for _, p := range mappedOutputPaths(string(raw), data) {
		if info, err := os.Stat(p); err == nil && info.IsDir() {
			_ = e.addOutputMount(p, false)
		}
	}
}

func (e *Engine) outputLocation(value string) (*os.Root, string, string, error) {
	value = strings.ReplaceAll(strings.TrimSpace(value), `\`, "/")
	if value == "" || strings.ContainsRune(value, 0) {
		return nil, "", "", errors.New("请选择 STRM 输出目录")
	}
	for _, part := range strings.Split(value, "/") {
		if part == ".." || part == "." {
			return nil, "", "", errors.New("输出路径不能包含 . 或 ..")
		}
	}
	native := filepath.FromSlash(value)
	if !filepath.IsAbs(native) {
		if _, err := safeRelative(value); err != nil {
			return nil, "", "", err
		}
		for _, m := range e.outputMounts {
			if m.Default {
				native = filepath.Join(filepath.FromSlash(m.Path), native)
				break
			}
		}
	}
	native = filepath.Clean(native)
	var chosen *outputMount
	for i := range e.outputMounts {
		m := &e.outputMounts[i]
		if withinPath(native, filepath.FromSlash(m.Path)) && (chosen == nil || len(m.Path) > len(chosen.Path)) {
			chosen = m
		}
	}
	if chosen == nil {
		return nil, "", "", errors.New("该路径不在可用的输出映射内，请点击选择目录")
	}
	rel, err := filepath.Rel(filepath.FromSlash(chosen.Path), native)
	if err != nil {
		return nil, "", "", err
	}
	key := filepath.ToSlash(native)
	if chosen.Default {
		key = rel
	}
	return chosen.root, rel, key, nil
}

func (e *Engine) normalizeOutputDirectory(value string) (string, error) {
	value = strings.ReplaceAll(strings.TrimSpace(value), `\`, "/")
	r, rel, _, err := e.outputLocation(value)
	if err != nil {
		return "", err
	}
	parent := rel
	for {
		info, er := r.Stat(parent)
		if er == nil {
			if !info.IsDir() {
				return "", errors.New("输出路径不是目录")
			}
			break
		}
		if !os.IsNotExist(er) {
			return "", errors.New("输出目录不可访问或超出映射范围")
		}
		if parent == "." {
			return "", errors.New("输出映射不可读取")
		}
		parent = filepath.Dir(parent)
	}
	if filepath.IsAbs(filepath.FromSlash(value)) {
		return filepath.ToSlash(filepath.Clean(filepath.FromSlash(value))), nil
	}
	return safeRelative(value)
}

func (e *Engine) openOutput(value string) (*os.File, error) {
	r, rel, _, err := e.outputLocation(value)
	if err != nil {
		return nil, err
	}
	return r.Open(rel)
}

func (s *Server) outputDirectories(w http.ResponseWriter, r *http.Request) {
	value := r.URL.Query().Get("path")
	if value == "" {
		jsonOut(w, 200, object{"mounts": s.engine.outputMounts, "entries": []object{}})
		return
	}
	if !filepath.IsAbs(filepath.FromSlash(value)) {
		fail(w, 400, "请选择完整输出路径")
		return
	}
	root, rel, _, err := s.engine.outputLocation(value)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	f, err := root.Open(rel)
	if err != nil {
		fail(w, 400, "目录不可读取，请检查映射和权限")
		return
	}
	defer f.Close()
	entries, err := f.ReadDir(-1)
	if err != nil {
		fail(w, 400, "无法读取子目录")
		return
	}
	result := []object{}
	for _, entry := range entries {
		if entry.Type()&fs.ModeSymlink != 0 || !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		result = append(result, object{"name": entry.Name(), "path": filepath.ToSlash(filepath.Join(filepath.FromSlash(value), entry.Name()))})
	}
	sort.Slice(result, func(i, j int) bool { return result[i]["name"].(string) < result[j]["name"].(string) })
	jsonOut(w, 200, object{"path": filepath.ToSlash(filepath.Clean(filepath.FromSlash(value))), "entries": result})
}
