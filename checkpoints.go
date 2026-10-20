package main

import (
	"database/sql"
	"encoding/json"
	"errors"
)

// Directory data is immutable while its cursor advances. Keep the large
// snapshot, scalar cursor and incremental output claims in separate rows so a
// file-sized progress update does not rewrite a whole directory/tree.
func saveCheckpointTx(tx *sql.Tx, job string, cp *ScanCheckpoint) error {
	if err := validateCheckpoint(cp); err != nil {
		return err
	}
	if !cp.persisted || cp.structureDirty {
		disk := *cp
		disk.Format, disk.Snapshot = 1, cp.Files != nil
		disk.Files, disk.Claimed, disk.Index = nil, nil, 0
		raw, err := json.Marshal(disk)
		if err != nil {
			return err
		}
		if _, err = tx.Exec("INSERT INTO job_checkpoints(job,payload) VALUES(?,?) ON CONFLICT(job) DO UPDATE SET payload=excluded.payload", job, string(raw)); err != nil {
			return err
		}
	}
	if !cp.persisted || cp.filesDirty {
		if cp.Files != nil {
			raw, err := json.Marshal(cp.Files)
			if err != nil {
				return err
			}
			if _, err = tx.Exec("INSERT INTO checkpoint_snapshots(job,folder,payload) VALUES(?,?,?) ON CONFLICT(job) DO UPDATE SET folder=excluded.folder,payload=excluded.payload", job, cp.Current.ID, string(raw)); err != nil {
				return err
			}
		} else if _, err := tx.Exec("DELETE FROM checkpoint_snapshots WHERE job=?", job); err != nil {
			return err
		}
	}
	if !cp.persisted {
		// Legacy checkpoints carry all claims inline. Convert them atomically
		// with their cursor and snapshot on the next successful progress save.
		if _, err := tx.Exec("DELETE FROM checkpoint_claims WHERE job=?", job); err != nil {
			return err
		}
		for path, media := range cp.Claimed {
			if _, err := tx.Exec("INSERT INTO checkpoint_claims VALUES(?,?,?)", job, path, media); err != nil {
				return err
			}
		}
	} else if cp.claimPath != "" {
		if _, err := tx.Exec("INSERT INTO checkpoint_claims VALUES(?,?,?) ON CONFLICT(job,path) DO UPDATE SET media=excluded.media", job, cp.claimPath, cp.claimMedia); err != nil {
			return err
		}
	}
	_, err := tx.Exec("INSERT INTO checkpoint_cursors VALUES(?,?) ON CONFLICT(job) DO UPDATE SET position=excluded.position", job, cp.Index)
	return err
}

func validateCheckpoint(cp *ScanCheckpoint) error {
	if cp.Format < 0 || cp.Format > 1 || cp.Index < 0 || cp.Index > len(cp.Files) || cp.Current == nil && (cp.Files != nil || cp.Index != 0) || cp.Current != nil && cp.Current.ID == "" {
		return errors.New("断点内容或游标无效，未执行恢复，请重新生成")
	}
	return nil
}

func (s *Store) loadCheckpoint(job string, cp *ScanCheckpoint) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var raw string
	if err = tx.QueryRow("SELECT payload FROM job_checkpoints WHERE job=?", job).Scan(&raw); err != nil {
		return err
	}
	var disk ScanCheckpoint
	if json.Unmarshal([]byte(raw), &disk) != nil {
		return errors.New("断点读取失败，请重新生成")
	}
	// All historic checkpoints were marshaled from ScanCheckpoint and contain
	// these fields even when their values are null/zero. Valid JSON alone is
	// insufficient: {} / null must not replace the initialized root worklist.
	var fields map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &fields) != nil || fields == nil {
		return errors.New("断点缺少必要状态，未执行恢复")
	}
	for _, name := range []string{"Todo", "Current", "Files", "Index", "Visited", "Claimed"} {
		if _, ok := fields[name]; !ok {
			return errors.New("断点缺少必要状态，未执行恢复")
		}
	}
	if disk.Format == 1 {
		if disk.Files != nil || disk.Claimed != nil || disk.Index != 0 {
			return errors.New("断点格式不一致，未执行恢复")
		}
		if err = tx.QueryRow("SELECT position FROM checkpoint_cursors WHERE job=?", job).Scan(&disk.Index); err != nil {
			return errors.New("断点游标缺失，未执行恢复")
		}
		if disk.Snapshot {
			var folder, data string
			if err = tx.QueryRow("SELECT folder,payload FROM checkpoint_snapshots WHERE job=?", job).Scan(&folder, &data); err != nil || disk.Current == nil || folder != disk.Current.ID || json.Unmarshal([]byte(data), &disk.Files) != nil || disk.Files == nil {
				return errors.New("断点目录快照无效，未执行恢复")
			}
		}
		disk.Claimed = map[string]string{}
		rows, err := tx.Query("SELECT path,media FROM checkpoint_claims WHERE job=?", job)
		if err != nil {
			return err
		}
		for rows.Next() {
			var path, media string
			if err = rows.Scan(&path, &media); err != nil {
				rows.Close()
				return err
			}
			disk.Claimed[path] = media
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		disk.persisted = true
	}
	if err = validateCheckpoint(&disk); err != nil {
		return err
	}
	for _, dir := range disk.Todo {
		if dir.ID == "" {
			return errors.New("断点待处理目录身份无效，未执行恢复")
		}
		if _, err := validRemotePath(dir.Source); err != nil {
			return errors.New("断点待处理目录路径无效，未执行恢复")
		}
	}
	if disk.Current != nil {
		if _, err := validRemotePath(disk.Current.Source); err != nil {
			return errors.New("断点当前目录路径无效，未执行恢复")
		}
	}
	seen := make(map[string]bool, len(disk.Files))
	for _, file := range disk.Files {
		if file.ID == "" || file.Name == "" || file.Size < 0 || seen[file.ID] {
			return errors.New("断点文件身份无效或重复，未执行恢复")
		}
		seen[file.ID] = true
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	*cp = disk
	return nil
}
