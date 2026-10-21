package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
)

// An empty scoped snapshot must remember which positive identity it retired.
// Its authority ends at that positive publication, so a newer sibling remains
// valid. These bounded facts are carried by the current baseline itself; they
// never keep a chain of historical runs alive.
type scanTombstone struct {
	Observation scanObservation `json:"observation"`
	Publication int64           `json:"publication"`
}

func mergeScanTombstonesTx(tx *sql.Tx, identity scanIdentity, baseline scanBaseline, remote string) error {
	query := "SELECT remote,before_payload FROM scan_changes WHERE run=? AND kind='source_tombstone'"
	args := []any{baseline.Run}
	if remote != "" {
		query += " AND remote=?"
		args = append(args, remote)
	}
	rows, err := tx.Query(query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, raw string
		var v scanTombstone
		if err = rows.Scan(&id, &raw); err != nil {
			return err
		}
		if restoreDecode([]byte(raw), &v) != nil || !validScanObservation(v.Observation) || v.Observation.Entry.ID != id || !scanSourceCovered(baseline.Scope, v.Observation.Source) || v.Publication <= 0 || v.Publication > baseline.Publication {
			return errors.New("扫描缺失观测的身份或发布顺序无效")
		}
		if !scanSourceCovered(identity.Source, v.Observation.Source) {
			continue
		}
		payload, _ := json.Marshal(v.Observation)
		if _, err = tx.Exec(`INSERT INTO scan_tombstones VALUES(?,?,?,?) ON CONFLICT(remote) DO UPDATE SET source=excluded.source,payload=excluded.payload,publication=excluded.publication WHERE excluded.publication>scan_tombstones.publication`, id, v.Observation.Source, string(payload), v.Publication); err != nil {
			return err
		}
	}
	return rows.Err()
}

// Both reconciliation and repair resolve identity through the same positive /
// negative overlay. A repair asks for just one remote ID, bounding its work.
// The caller drops both temporary tables before returning or committing.
func buildScanComparisonTx(tx *sql.Tx, identity scanIdentity, baselines []scanBaseline, remote string) error {
	if _, err := tx.Exec(`CREATE TEMP TABLE scan_compare(remote TEXT PRIMARY KEY,source TEXT NOT NULL,payload TEXT NOT NULL,publication INTEGER NOT NULL);
CREATE TEMP TABLE scan_tombstones(remote TEXT PRIMARY KEY,source TEXT NOT NULL,payload TEXT NOT NULL,publication INTEGER NOT NULL);`); err != nil {
		return err
	}
	for _, b := range baselines {
		if !b.Compare {
			continue
		}
		prefix := strings.TrimSuffix(b.Scope, "/") + "/"
		if _, err := tx.Exec("DELETE FROM scan_compare WHERE source=? OR substr(source,1,length(?))=?", b.Scope, prefix, prefix); err != nil {
			return err
		}
		if err := mergeScanTombstonesTx(tx, identity, b, remote); err != nil {
			return err
		}
		if _, err := tx.Exec("DELETE FROM scan_compare WHERE EXISTS (SELECT 1 FROM scan_tombstones t WHERE t.remote=scan_compare.remote AND t.publication>=scan_compare.publication)"); err != nil {
			return err
		}
		comparePrefix := strings.TrimSuffix(identity.Source, "/") + "/"
		query := `INSERT INTO scan_compare SELECT remote,source,payload,? FROM scan_entries WHERE run=? AND (source=? OR substr(source,1,length(?))=?)`
		args := []any{b.Publication, b.Run, identity.Source, comparePrefix, comparePrefix}
		if remote != "" {
			query += " AND remote=?"
			args = append(args, remote)
		}
		query += " ON CONFLICT(remote) DO UPDATE SET source=excluded.source,payload=excluded.payload,publication=excluded.publication"
		if _, err := tx.Exec(query, args...); err != nil {
			return err
		}
	}
	return nil
}

func dropScanComparisonTx(tx *sql.Tx) error {
	_, err := tx.Exec("DROP TABLE IF EXISTS scan_compare; DROP TABLE IF EXISTS scan_tombstones")
	return err
}

func repairScanObservationTx(tx *sql.Tx, identity scanIdentity, baselines []scanBaseline, remote string) (scanObservation, error) {
	var observation scanObservation
	if err := buildScanComparisonTx(tx, identity, baselines, remote); err != nil {
		return observation, errors.Join(err, dropScanComparisonTx(tx))
	}
	var raw string
	err := tx.QueryRow("SELECT payload FROM scan_compare WHERE remote=?", remote).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	} else if err == nil {
		err = restoreDecode([]byte(raw), &observation)
	}
	return observation, errors.Join(err, dropScanComparisonTx(tx))
}
