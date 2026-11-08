package store

import (
	"database/sql"
	"embed"
	"fmt"
	"sync/atomic"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

//go:embed testdata/seed.sql
var testFixtures embed.FS

// uniqueDB counter so each openTestStore call gets an isolated shared-cache DB.
var uniqueDBCounter atomic.Uint64

// openTestStore opens an in-memory store, applies the schema and seeds fixtures.
// The returned Store is registered for cleanup via t.Cleanup. The whatsmeowDB
// handle points at the same in-memory DB (the seeded whatsmeow_lid_map table
// lives there).
func openTestStore(t *testing.T) *Store {
	t.Helper()

	// Use a uniquely-named in-memory DB with shared cache so repeat opens
	// across tests don't collide. `mode=memory&cache=shared` with a named
	// file makes the DB persist across connections in the same process.
	id := uniqueDBCounter.Add(1)
	dsn := fmt.Sprintf("file:store_test_%d?mode=memory&cache=shared&_foreign_keys=on", id)

	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		t.Fatalf("open in-memory db: %v", err)
	}
	// A single connection to avoid per-connection isolation weirdness with
	// shared-cache in-memory databases.
	db.SetMaxOpenConns(1)

	seed, err := testFixtures.ReadFile("testdata/seed.sql")
	if err != nil {
		db.Close()
		t.Fatalf("read seed.sql: %v", err)
	}
	if _, err := db.Exec(string(seed)); err != nil {
		db.Close()
		t.Fatalf("apply seed: %v", err)
	}

	// A second handle to the same shared-cache DB, playing the role of
	// whatsmeowDB. LID lookups hit the same underlying tables.
	waDB, err := sql.Open("sqlite3", dsn)
	if err != nil {
		db.Close()
		t.Fatalf("open whatsmeow db handle: %v", err)
	}
	waDB.SetMaxOpenConns(1)

	s := &Store{
		db:          db,
		whatsmeowDB: waDB,
		dir:         t.TempDir(),
	}
	t.Cleanup(func() {
		_ = s.Close()
	})
	return s
}
