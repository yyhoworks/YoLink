package main

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"filippo.io/age"
)

const (
	backupFormatVersion = 1
	backupMaxFiles      = 100000
	backupMaxDirs       = 100000
	backupMaxPathBytes  = 4096
	backupMaxFileBytes  = int64(2 << 30)
	backupMaxTotalBytes = int64(4 << 30)
	backupMaxManifest   = int64(32 << 20)
	backupScryptWork    = 18 // age default, approximately 256 MiB; one operation at a time
)

type backupFile struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type backupManifest struct {
	Format  int          `json:"format"`
	Version string       `json:"version"`
	Schema  int          `json:"schema"`
	Created int64        `json:"created"`
	Files   []backupFile `json:"files"`
}

// Only application data is restorable. No compose/env files, output media,
// absolute paths, symbolic links or executable scripts are accepted.
func backupNameAllowed(name string) bool {
	if name == "" || len(name) > backupMaxPathBytes || !utf8.ValidString(name) || path.Clean(name) != name || strings.ContainsAny(name, "\\:\x00") || strings.HasPrefix(name, "/") {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if part == "." || part == ".." || strings.TrimRight(part, ". ") != part {
			return false
		}
		base := strings.ToUpper(strings.SplitN(part, ".", 2)[0])
		if base == "CON" || base == "PRN" || base == "AUX" || base == "NUL" || len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) && base[3] >= '0' && base[3] <= '9' {
			return false
		}
		for _, c := range part {
			if c < 32 || strings.ContainsRune(`<>"|?*`, c) {
				return false
			}
		}
	}
	switch name {
	case "strmlink.db", "credentials.key", "playback.key", "events.key", "admin-credential.json", "initial-admin-password.txt":
		return true
	}
	parts := strings.Split(name, "/")
	validID := func(id string) bool { return len(id) == 32 && strings.Trim(id, "0123456789abcdef") == "" }
	if len(parts) == 2 && parts[0] == "plans" {
		ext := path.Ext(parts[1])
		return (ext == ".json" || ext == ".progress") && validID(strings.TrimSuffix(parts[1], ext))
	}
	return len(parts) >= 3 && parts[0] == "strm-backups" && validID(parts[1]) && strings.HasSuffix(name, ".strm")
}

// Reserve every distinct parent BEFORE MkdirAll creates any of them. File count
// alone does not bound directories: each allowed path can introduce many parents.
// The restore inventory has the same directory bound. Existing deep paths remain
// supported; this limit is on the aggregate number of directories, not depth.
type backupParents map[string]bool

func (parents backupParents) admit(name string) error {
	for dir := path.Dir(name); dir != "."; dir = path.Dir(dir) {
		if parents[dir] {
			continue
		}
		if len(parents) >= backupMaxDirs {
			return errors.New("备份父目录数量超过限制")
		}
		parents[dir] = true
	}
	return nil
}

type backupContextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r backupContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

func backupFileDigest(ctx context.Context, root *os.Root, name string) (backupFile, error) {
	info, err := root.Lstat(name)
	if err != nil || !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > backupMaxFileBytes {
		return backupFile{}, errors.New("备份包含无法读取、过大或非普通文件")
	}
	f, err := root.Open(name)
	if err != nil {
		return backupFile{}, errors.New("备份文件读取失败")
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(backupContextReader{ctx, f}, info.Size()+1))
	if err != nil || n != info.Size() {
		return backupFile{}, errors.New("备份期间文件发生变化或读取中断")
	}
	return backupFile{name, n, hex.EncodeToString(h.Sum(nil))}, nil
}

func validateBackupManifest(m backupManifest) error {
	if m.Format != backupFormatVersion || m.Schema < 1 || m.Schema > currentSchemaVersion || m.Created <= 0 || len(m.Version) > 64 || len(m.Files) < 4 || len(m.Files) > backupMaxFiles {
		return errors.New("备份版本不受支持或清单无效")
	}
	seen := make(map[string]bool)
	parents := backupParents{}
	var total int64
	for _, f := range m.Files {
		key := strings.ToLower(f.Name)
		if !backupNameAllowed(f.Name) || seen[key] || f.Size < 0 || f.Size > backupMaxFileBytes || len(f.SHA256) != 64 || strings.Trim(f.SHA256, "0123456789abcdef") != "" {
			return errors.New("备份清单包含不安全路径、重复文件或无效摘要")
		}
		seen[key] = true
		if err := parents.admit(f.Name); err != nil {
			return err
		}
		total += f.Size
		if total > backupMaxTotalBytes {
			return errors.New("备份解包大小超过限制")
		}
		if strings.HasSuffix(f.Name, ".key") && f.Size != 32 {
			return errors.New("备份密钥长度无效")
		}
		if (f.Name == "admin-credential.json" || f.Name == "initial-admin-password.txt") && (f.Size < 1 || f.Size > 4096) {
			return errors.New("备份登录配置大小无效")
		}
	}
	for _, required := range []string{"strmlink.db", "credentials.key", "playback.key", "events.key"} {
		if !seen[required] {
			return errors.New("备份缺少数据库或必要密钥")
		}
	}
	if !seen["admin-credential.json"] && !seen["initial-admin-password.txt"] {
		return errors.New("备份缺少可恢复的管理员登录配置")
	}
	return nil
}

// source must be a private stable snapshot directory, never a live data tree.
// The caller must discard destination on ANY error (no valid partial backup).
func writeBackupArchive(ctx context.Context, dst io.Writer, source, password string, schema int) (backupManifest, error) {
	m := backupManifest{Format: backupFormatVersion, Version: appVersion, Schema: schema, Created: time.Now().Unix()}
	if len(password) < 12 || len(password) > 1024 {
		return m, errors.New("备份口令需为 12–1024 字节，请使用独立的长口令")
	}
	root, err := os.OpenRoot(source)
	if err != nil {
		return m, errors.New("备份快照无法读取")
	}
	defer root.Close()
	err = filepath.WalkDir(source, func(name string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return errors.New("备份快照遍历失败")
		}
		if entry.IsDir() {
			return nil
		}
		rel, e := filepath.Rel(source, name)
		if e != nil || !backupNameAllowed(filepath.ToSlash(rel)) || len(m.Files) >= backupMaxFiles {
			return errors.New("备份快照包含非托管文件或文件过多")
		}
		f, e := backupFileDigest(ctx, root, filepath.ToSlash(rel))
		if e != nil {
			return e
		}
		m.Files = append(m.Files, f)
		return nil
	})
	if err != nil {
		return m, err
	}
	sort.Slice(m.Files, func(i, j int) bool { return m.Files[i].Name < m.Files[j].Name })
	if err = validateBackupManifest(m); err != nil {
		return m, err
	}
	raw, err := json.Marshal(m)
	if err != nil || int64(len(raw)) > backupMaxManifest {
		return m, errors.New("备份清单过大")
	}
	recipient, err := age.NewScryptRecipient(password)
	if err != nil {
		return m, errors.New("备份口令无效")
	}
	recipient.SetWorkFactor(backupScryptWork)
	encrypted, err := age.Encrypt(dst, recipient)
	if err != nil {
		return m, errors.New("备份加密初始化失败")
	}
	tw := tar.NewWriter(encrypted)
	if err = tw.WriteHeader(&tar.Header{Name: "manifest.json", Mode: 0600, Size: int64(len(raw)), Typeflag: tar.TypeReg}); err == nil {
		_, err = tw.Write(raw)
	}
	for _, file := range m.Files {
		if err != nil {
			break
		}
		var f *os.File
		f, err = root.Open(file.Name)
		if err != nil {
			break
		}
		err = tw.WriteHeader(&tar.Header{Name: file.Name, Mode: 0600, Size: file.Size, Typeflag: tar.TypeReg})
		if err == nil {
			h := sha256.New()
			var n int64
			n, err = io.Copy(io.MultiWriter(tw, h), io.LimitReader(backupContextReader{ctx, f}, file.Size+1))
			if err == nil && (n != file.Size || hex.EncodeToString(h.Sum(nil)) != file.SHA256) {
				err = errors.New("快照变化")
			}
		}
		f.Close()
	}
	if err == nil {
		err = tw.Close()
	}
	if err == nil {
		err = encrypted.Close()
	}
	if err != nil || ctx.Err() != nil {
		return m, errors.New("备份写入中断或快照变化，未生成有效备份")
	}
	return m, nil
}

// stageBackupArchive always creates a new private directory. No caller-selected
// restore destination is opened or overwritten. Caller removes failed staging.
func stageBackupArchive(ctx context.Context, src io.Reader, parent, password string) (dir string, manifest backupManifest, err error) {
	if len(password) < 12 || len(password) > 1024 {
		return "", manifest, errors.New("备份口令长度无效")
	}
	identity, _ := age.NewScryptIdentity(password)
	identity.SetMaxWorkFactor(backupScryptWork)
	// Bound ciphertext/header processing too, not only manifest-declared sizes.
	bounded := io.LimitReader(backupContextReader{ctx, src}, backupMaxTotalBytes+backupMaxManifest+int64(backupMaxFiles)*4096)
	plain, err := age.Decrypt(bounded, identity)
	if err != nil {
		return "", manifest, errors.New("备份口令错误、加密文件损坏或加密参数不受支持")
	}
	tr := tar.NewReader(plain)
	header, err := tr.Next()
	if err != nil || header.Name != "manifest.json" || header.Typeflag != tar.TypeReg || header.Size <= 0 || header.Size > backupMaxManifest {
		return "", manifest, errors.New("备份清单缺失或过大")
	}
	raw, err := io.ReadAll(tr)
	if err != nil || validateBackupJSON(raw) != nil {
		return "", manifest, errors.New("备份清单读取失败")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&manifest) != nil || decoder.Decode(new(any)) != io.EOF {
		return "", manifest, errors.New("备份清单格式无效")
	}
	if err = validateBackupManifest(manifest); err != nil {
		return "", manifest, err
	}
	dir, err = os.MkdirTemp(parent, ".restore-stage-")
	if err != nil {
		return "", manifest, errors.New("无法创建隔离恢复目录")
	}
	defer func() {
		if err != nil {
			// Exact directory freshly created above; no user path is removed.
			if cleanupErr := os.RemoveAll(dir); cleanupErr != nil {
				err = errors.Join(err, errors.New("隔离目录清理失败，需管理员处理遗留恢复资料"))
			} else {
				dir = ""
			}
		}
	}()
	root, err := os.OpenRoot(dir)
	if err != nil {
		return dir, manifest, errors.New("隔离恢复目录无法打开")
	}
	defer root.Close()
	for _, expected := range manifest.Files {
		header, err = tr.Next()
		if err != nil || header.Name != expected.Name || header.Typeflag != tar.TypeReg || header.Size != expected.Size || header.Linkname != "" {
			return dir, manifest, errors.New("备份文件与清单不一致或包含链接")
		}
		if err = root.MkdirAll(path.Dir(expected.Name), 0700); err != nil {
			return dir, manifest, errors.New("恢复目录创建失败")
		}
		f, openErr := root.OpenFile(expected.Name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if openErr != nil {
			return dir, manifest, errors.New("恢复文件无法安全创建")
		}
		h := sha256.New()
		n, copyErr := io.Copy(io.MultiWriter(f, h), backupContextReader{ctx, tr})
		syncErr := f.Sync()
		closeErr := f.Close()
		if copyErr != nil || syncErr != nil || closeErr != nil || n != expected.Size || hex.EncodeToString(h.Sum(nil)) != expected.SHA256 {
			return dir, manifest, errors.New("备份完整性校验失败或恢复写入中断")
		}
	}
	if _, err = tr.Next(); err != io.EOF {
		return dir, manifest, errors.New("备份包含清单之外的内容或尾部损坏")
	}
	// tar EOF is NOT age EOF. Drain and authenticate the final encrypted chunk;
	// reject bytes after the archive rather than accepting a valid prefix.
	n, tailErr := io.Copy(io.Discard, backupContextReader{ctx, plain})
	if tailErr != nil || n != 0 || ctx.Err() != nil {
		return dir, manifest, errors.New("备份尾部认证失败或存在多余内容")
	}
	return dir, manifest, nil
}
