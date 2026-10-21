package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// All names below are fixed application schema identifiers. Bound EVERY field
// used by history/UI/quarantine before reading restored rows into Go.
type scanTableSpec struct {
	name     string
	text     map[string]int
	integers []string
	json     []string
}

func scanTableSpecs() []scanTableSpec {
	return []scanTableSpec{
		{"scan_runs", map[string]int{"id": 128, "rule": 128, "scope": 32768, "identity": 128, "status": 64, "payload": 1 << 20}, []string{"created", "completed", "bytes"}, []string{"payload"}},
		{"scan_entries", map[string]int{"run": 128, "remote": 4096, "source": 32768, "payload": 1 << 20, "listed": 128}, []string{"done"}, []string{"payload"}},
		{"scan_baselines", map[string]int{"rule": 128, "scope": 32768, "run": 128, "identity": 128}, []string{"completed", "full_completed"}, nil},
		{"scan_changes", map[string]int{"run": 128, "remote": 4096, "kind": 64, "before_payload": 1 << 20, "after_payload": 1 << 20}, nil, []string{"before_payload", "after_payload"}},
		{"scan_items", map[string]int{"run": 128, "remote": 4096, "state": 64, "payload": 1 << 20}, nil, []string{"payload"}},
		{"change_plans", map[string]int{"id": 128, "state": 64, "payload": 1 << 20}, []string{"created"}, []string{"payload"}},
		{"change_items", map[string]int{"plan": 128, "state": 64, "payload": 1 << 20}, []string{"position"}, []string{"payload"}},
	}
}
func validateBackupScanRows(ctx context.Context, db *sql.DB) error {
	for _, t := range scanTableSpecs() {
		checks, bytes := []string{}, []string{}
		for column, limit := range t.text {
			checks = append(checks, fmt.Sprintf("typeof(%s)<>'text' OR length(CAST(%s AS BLOB))>%d", column, column, limit))
			if column == "state" || column == "status" {
				bytes = append(bytes, "64")
			} else {
				bytes = append(bytes, "length(CAST("+column+" AS BLOB))")
			}
		}
		for _, column := range t.integers {
			checks = append(checks, "typeof("+column+")<>'integer'")
		}
		for _, column := range t.json {
			condition := "NOT json_valid(" + column + ")"
			if t.name == "scan_changes" {
				condition = "(" + column + "<>'' AND " + condition + ")"
			}
			checks = append(checks, condition)
		}
		var invalid int
		var count, total int64
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*),COALESCE(SUM("+strings.Join(bytes, "+")+"),0) FROM "+t.name).Scan(&count, &total); err != nil || count > scanTableRows || total > scanTableBytes {
			return errors.New("备份扫描或修复记录总量过大")
		}
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+t.name+" WHERE "+strings.Join(checks, " OR ")).Scan(&invalid); err != nil || invalid != 0 {
			return errors.New("备份扫描或修复字段类型、大小或格式无效")
		}
		var savedCount, savedBytes int64
		if err := db.QueryRowContext(ctx, "SELECT rows,bytes FROM scan_storage WHERE name=?", t.name).Scan(&savedCount, &savedBytes); err != nil || savedCount != count || savedBytes != total {
			return errors.New("备份扫描容量记录不一致")
		}
	}
	var quotaRows int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM scan_storage").Scan(&quotaRows); err != nil || quotaRows != len(scanTableSpecs()) {
		return errors.New("备份扫描容量表无效")
	}
	return nil
}
