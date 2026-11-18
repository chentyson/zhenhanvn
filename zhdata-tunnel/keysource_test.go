package main

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func makeSuiDB(t *testing.T, uuids ...string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "s-ui.db")
	db, err := sql.Open("sqlite", p)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(`CREATE TABLE client (id integer primary key, uuid text, name text);
		CREATE TABLE settings (key text, value text);`); err != nil {
		t.Fatal(err)
	}
	for _, u := range uuids {
		if _, err = db.Exec(`INSERT INTO client (uuid,name) VALUES (?,?)`, u, "user"); err != nil {
			t.Fatal(err)
		}
	}
	return p
}

func TestReadUsersFromDB(t *testing.T) {
	p := makeSuiDB(t, "UPPER-Aaa", "b-bb")
	u, err := readUsersFromDB(p)
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(u)
	if len(u) != 2 || u[0] != "b-bb" || u[1] != "upper-aaa" {
		t.Fatalf("unexpected users: %v", u)
	}
}

func TestReadUsersFromDBNoUuidTable(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.db")
	db, _ := sql.Open("sqlite", p)
	db.Exec(`CREATE TABLE foo (a text)`)
	db.Close()
	if _, err := readUsersFromDB(p); err == nil {
		t.Fatal("expected error when no uuid column exists")
	}
}

func TestKeyStoreUnionDedupHotReloadFailSafe(t *testing.T) {
	dbp := makeSuiDB(t, "uuid-in-db")
	txtp := filepath.Join(t.TempDir(), "u.txt")
	os.WriteFile(txtp, []byte("uuid-in-db\nextra-key\n"), 0o644)

	ks, err := newKeyStore(dbp + "," + txtp)
	if err != nil {
		t.Fatal(err)
	}
	k := ks.snapshot()
	sort.Strings(k)
	if len(k) != 2 || k[0] != "extra-key" || k[1] != "uuid-in-db" {
		t.Fatalf("union/dedup wrong: %v", k)
	}

	// add a user in the DB (s-ui adding a client), no process restart
	db, err := sql.Open("sqlite", dbp)
	if err != nil {
		t.Fatal(err)
	}
	db.Exec(`INSERT INTO client (uuid,name) VALUES ('new-uuid','x')`)
	db.Close()
	if !ks.reloadIfChanged() {
		t.Fatal("expected reload after db change")
	}
	k = ks.snapshot()
	if !contains(k, "new-uuid") || len(k) != 3 {
		t.Fatalf("hot reload missed new uuid: %v", k)
	}

	// db file vanishes -> fall back to txt only
	os.Remove(dbp)
	if !ks.reloadIfChanged() {
		t.Fatal("expected reload after db removed")
	}
	k = ks.snapshot()
	if len(k) != 2 || contains(k, "new-uuid") {
		t.Fatalf("expected txt-only keys, got: %v", k)
	}

	// all sources gone -> fail-safe keeps previous keys
	os.Remove(txtp)
	ks.reloadIfChanged()
	k = ks.snapshot()
	if len(k) != 2 {
		t.Fatalf("fail-safe broken, keys now: %v", k)
	}
}

func TestEndToEndDBUsers(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("db-users-ok"))
	}))
	defer target.Close()
	host, port := splitHostPort(t, target.URL)

	dbp := makeSuiDB(t, "sui-user-001")
	srv, err := newServerApp(serverConfig{
		listen:    "127.0.0.1:0",
		usersPath: dbp,
		backend:   "direct",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	cli, err := newClientApp(clientConfig{listen: "127.0.0.1:0", server: srv.Addr(), uuid: "sui-user-001"})
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	waitFor(t, func() bool { return srv.estab.Load() >= 1 }, "db-user session")

	body, err := httpThroughTunnel(t, cli.Addr(), host, port)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "db-users-ok") {
		t.Fatalf("bad body %q", body)
	}
}

func contains(s []string, x string) bool {
	for _, v := range s {
		if v == x {
			return true
		}
	}
	return false
}
