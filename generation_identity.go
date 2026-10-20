package main

import "encoding/json"

// An in-place migration uses a stable path-derived ID. Reuse that ID only when
// this output already owns the same account, exact source path and cloud file.
// publish still validates the existing bytes and ownership; nothing is replaced.
func (e *Engine) mediaForGeneration(a Account, f Entry, source, dest string) Media {
	m := Media{ID: e.s.mediaID(a, f.ID), Account: a.ID, RemoteID: f.ID, PickCode: f.PickCode, Name: f.Name, Path: source, Size: f.Size}
	_, _, key, err := e.outputLocation(dest)
	if err != nil {
		return m
	}
	var raw string
	if err = e.s.db.QueryRow("SELECT m.payload FROM outputs o JOIN media m ON m.id=o.media WHERE o.path=?", key).Scan(&raw); err != nil {
		return m
	}
	var old Media
	if json.Unmarshal([]byte(raw), &old) == nil && old.Legacy && old.Account == a.ID && old.Path == source && old.RemoteID != "" && old.RemoteID == f.ID {
		m.ID = old.ID
		m.Legacy = true
	}
	return m
}
