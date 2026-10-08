// Package opencode opens opencode's SQLite database read-only. The board's
// adapter and the usage ledger both read it.
//
// opencode keeps every session, message and part in one database under
// $XDG_DATA_HOME/opencode (default ~/.local/share/opencode): opencode.db for
// release builds, opencode-<channel>.db for others, or $OPENCODE_DB. It runs
// in WAL mode, so reading while opencode writes is safe.
package opencode

import (
	"database/sql"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	_ "modernc.org/sqlite"
)

// DataDir is opencode's data directory for home ("" = the user's home).
func DataDir(home string) string {
	if d := os.Getenv("XDG_DATA_HOME"); d != "" {
		return filepath.Join(d, "opencode")
	}
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	return filepath.Join(home, ".local", "share", "opencode")
}

// DBPaths are the databases to read, most recently written first.
func DBPaths(home string) []string {
	dir := DataDir(home)
	if v := os.Getenv("OPENCODE_DB"); v != "" && v != ":memory:" {
		if !filepath.IsAbs(v) {
			v = filepath.Join(dir, v)
		}
		return []string{v}
	}
	m, _ := filepath.Glob(filepath.Join(dir, "opencode*.db"))
	sort.Slice(m, func(i, j int) bool { return mod(m[i]) > mod(m[j]) })
	return m
}

func mod(p string) int64 {
	var t int64
	if st, err := os.Stat(p); err == nil {
		t = st.ModTime().UnixNano()
	}
	// The WAL file moves first; the db only changes on checkpoint.
	if st, err := os.Stat(p + "-wal"); err == nil {
		t = max(t, st.ModTime().UnixNano())
	}
	return t
}

var (
	mu    sync.Mutex
	conns = map[string]*sql.DB{}
)

// Open returns a shared read-only handle on the database at path.
func Open(path string) (*sql.DB, error) {
	mu.Lock()
	defer mu.Unlock()
	if db, ok := conns[path]; ok {
		return db, nil
	}
	if _, err := os.Stat(path); err != nil {
		return nil, err
	}
	u := url.URL{Scheme: "file", Path: path}
	q := url.Values{}
	q.Set("mode", "ro")
	q.Add("_pragma", "busy_timeout(2000)")
	q.Add("_pragma", "query_only(1)")
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(2)
	conns[path] = db
	return db, nil
}

// HasTable reports whether the database has table name (older opencode
// versions lack the v2 tables).
func HasTable(db *sql.DB, name string) bool {
	var n int
	err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, name).Scan(&n)
	return err == nil && n > 0
}

// DefaultTitle reports whether t is a title opencode made up before the
// session had a real one ("New session - 2026-10-08T…").
func DefaultTitle(t string) bool {
	return strings.HasPrefix(t, "New session - ") || strings.HasPrefix(t, "Child session - ")
}
