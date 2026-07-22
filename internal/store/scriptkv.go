package store

import (
	"database/sql"
	"time"
)

// script_kv is the per-script key/value space executable pages get. It is
// keyed by the script's store path, so /wordle.gmi.lua and /poll.gmi.lua
// never see each other's data, and a script's keys vanish when it is deleted.
const scriptKVSchema = `
CREATE TABLE IF NOT EXISTS script_kv (
	script  TEXT NOT NULL,
	key     TEXT NOT NULL,
	value   BLOB NOT NULL,
	updated INTEGER NOT NULL,
	PRIMARY KEY (script, key)
);
`

// ScriptKVGet returns a stored value for a script, and whether it existed.
func (s *Store) ScriptKVGet(script, key string) (string, bool) {
	var v string
	err := s.db.QueryRow(`SELECT value FROM script_kv WHERE script = ? AND key = ?`, script, key).Scan(&v)
	if err != nil {
		return "", false
	}
	return v, true
}

// ScriptKVSet writes a value for a script.
func (s *Store) ScriptKVSet(script, key, value string) error {
	_, err := s.db.Exec(`INSERT INTO script_kv (script, key, value, updated) VALUES (?,?,?,?)
		ON CONFLICT(script, key) DO UPDATE SET value=excluded.value, updated=excluded.updated`,
		script, key, value, time.Now().Unix())
	return err
}

// ScriptKVDelete removes a value.
func (s *Store) ScriptKVDelete(script, key string) {
	_, _ = s.db.Exec(`DELETE FROM script_kv WHERE script = ? AND key = ?`, script, key)
}

// ScriptKVKeys lists a script's keys.
func (s *Store) ScriptKVKeys(script string) []string {
	rows, err := s.db.Query(`SELECT key FROM script_kv WHERE script = ? ORDER BY key`, script)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err == nil {
			out = append(out, k)
		}
	}
	return out
}

// dropScriptKV removes a script's whole key space, inside the transaction
// that deletes the script page.
func dropScriptKV(tx *sql.Tx, script string) error {
	_, err := tx.Exec(`DELETE FROM script_kv WHERE script = ?`, script)
	return err
}

// ScriptKVRow is one stored value, for backup and restore.
type ScriptKVRow struct {
	Script string `json:"script"`
	Key    string `json:"key"`
	Value  string `json:"value"`
}

// AllScriptKV returns every stored value, so a backup can carry the data
// that executable pages keep.
func (s *Store) AllScriptKV() ([]ScriptKVRow, error) {
	rows, err := s.db.Query(`SELECT script, key, value FROM script_kv ORDER BY script, key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ScriptKVRow
	for rows.Next() {
		var r ScriptKVRow
		if err := rows.Scan(&r.Script, &r.Key, &r.Value); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
