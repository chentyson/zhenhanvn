package main

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// keyStore manages user keys from multiple sources (plain text and s-ui
// sqlite databases). -users accepts a comma separated list of paths; each
// path is auto-detected (sqlite magic) and results are unioned, lowercased,
// deduplicated. Files are re-read when mtime/size changes (hot reload).
type keyStore struct {
	paths []string

	mu     sync.RWMutex
	keys   []string
	sigs   map[string]string // path -> "mtimeNano:size"
	loaded bool
}

const keyReloadInterval = 20 * time.Second

func splitPaths(spec string) []string {
	var out []string
	for _, p := range strings.Split(spec, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func newKeyStore(spec string) (*keyStore, error) {
	paths := splitPaths(spec)
	if len(paths) == 0 {
		return nil, fmt.Errorf("empty -users spec")
	}
	ks := &keyStore{paths: paths, sigs: map[string]string{}}
	keys, per, errs := ks.collect()
	for _, err := range errs {
		log.Printf("[users] warn: %v", err)
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("no user keys from %v", paths)
	}
	ks.keys = keys
	ks.reSigs()
	ks.loaded = true
	log.Printf("[users] loaded %d keys %v", len(keys), per)
	return ks, nil
}

func (ks *keyStore) snapshot() []string {
	ks.mu.RLock()
	defer ks.mu.RUnlock()
	return append([]string(nil), ks.keys...)
}

func (ks *keyStore) reSigs() {
	sigs := map[string]string{}
	for _, p := range ks.paths {
		if st, err := os.Stat(p); err == nil {
			sigs[p] = fmt.Sprintf("%d:%d", st.ModTime().UnixNano(), st.Size())
		}
	}
	ks.sigs = sigs
}

// reloadIfChanged re-reads sources if any file changed (or appeared/vanished).
// On total failure the previous key set is kept (fail-safe).
func (ks *keyStore) reloadIfChanged() bool {
	sigs := map[string]string{}
	changed := false
	for _, p := range ks.paths {
		if st, err := os.Stat(p); err == nil {
			s := fmt.Sprintf("%d:%d", st.ModTime().UnixNano(), st.Size())
			sigs[p] = s
			if ks.sigs[p] != s {
				changed = true
			}
		} else if _, existed := ks.sigs[p]; existed {
			changed = true // file vanished
		}
	}
	if !changed {
		return false
	}
	keys, per, errs := ks.collect()
	if len(keys) == 0 {
		log.Printf("[users] reload produced 0 keys, keeping previous (%d); errors: %v", len(ks.snapshot()), joinErrs(errs))
		ks.reSigs()
		return false
	}
	for _, err := range errs {
		log.Printf("[users] warn: %v", err)
	}
	ks.mu.Lock()
	ks.keys = keys
	ks.mu.Unlock()
	ks.sigs = sigs
	log.Printf("[users] hot-reloaded %d keys %v", len(keys), per)
	return true
}

func (ks *keyStore) collect() (keys []string, per map[string]int, errs []error) {
	set := map[string]bool{}
	per = map[string]int{}
	for _, p := range ks.paths {
		var ks2 []string
		var err error
		if isSqliteFile(p) {
			ks2, err = readUsersFromDB(p)
		} else {
			ks2, err = loadUsers(p)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", p, err))
			continue
		}
		per[p] = len(ks2)
		for _, k := range ks2 {
			set[k] = true
		}
	}
	keys = make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return
}

func joinErrs(errs []error) string {
	s := make([]string, 0, len(errs))
	for _, e := range errs {
		s = append(s, e.Error())
	}
	return strings.Join(s, "; ")
}

func isSqliteFile(p string) bool {
	f, err := os.Open(p)
	if err != nil {
		return false
	}
	defer f.Close()
	var magic [15]byte
	if _, err = f.Read(magic[:]); err != nil {
		return false
	}
	return string(magic[:]) == "SQLite format 3"
}

// readUsersFromDB reads keys from an s-ui style sqlite database by finding
// every table that has a "uuid" column (schema-adaptive, read-only).
func readUsersFromDB(path string) ([]string, error) {
	db, err := openReadonlyDB(path)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.Query("SELECT name FROM sqlite_master WHERE type='table'")
	if err != nil {
		return nil, err
	}
	var tables []string
	for rows.Next() {
		var t string
		if err = rows.Scan(&t); err != nil {
			rows.Close()
			return nil, err
		}
		tables = append(tables, t)
	}
	rows.Close()

	var out []string
	used := 0
	for _, t := range tables {
		if !tableHasColumn(db, t, "uuid") {
			continue
		}
		used++
		q := "SELECT uuid FROM " + quoteIdent(t)
		r2, err := db.Query(q)
		if err != nil {
			continue
		}
		for r2.Next() {
			var u sql.NullString
			if err = r2.Scan(&u); err != nil {
				break
			}
			k := strings.ToLower(strings.TrimSpace(u.String))
			if k == "" || k == "null" {
				continue
			}
			out = append(out, k)
		}
		r2.Close()
	}
	if used == 0 {
		return nil, fmt.Errorf("no table with a 'uuid' column in %s", path)
	}
	return out, nil
}

func openReadonlyDB(path string) (*sql.DB, error) {
	uri := "file:" + filepath.ToSlash(path) + "?mode=ro&_pragma=query_only(1)"
	db, err := sql.Open("sqlite", uri)
	if err == nil {
		if err = db.Ping(); err == nil {
			return db, nil
		}
		db.Close()
	}
	// fallback for drivers/URIs without mode=ro support
	db, err = sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	if err = db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func tableHasColumn(db *sql.DB, table, col string) bool {
	rows, err := db.Query("PRAGMA table_info(" + quoteIdent(table) + ")")
	if err != nil {
		return false
	}
	defer rows.Close()
	for rows.Next() {
		var (
			cid     int
			name    string
			ctype   string
			notnull int
			dflt    sql.NullString
			pk      int
		)
		if err = rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return false
		}
		if strings.EqualFold(name, col) {
			return true
		}
	}
	return false
}

func quoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}
