package store

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

const carbonara = `>> title: Carbonara
>> tags: italian

@spaghetti{400%g}

Boil. Toss.
`

func TestSyncDirLifecycle(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// --- first sync: empty directory ------------------------------
	if _, err := db.SyncDir(dir); err != nil {
		t.Fatal(err)
	}
	if recipes, _ := db.ListRecipes(); len(recipes) != 0 {
		t.Fatalf("expected empty store, got %d", len(recipes))
	}

	// --- add a file ------------------------------------------------
	write(t, dir, "carbonara.cook", carbonara)
	rep, err := db.SyncDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Added) != 1 || rep.Added[0] != "carbonara.cook" {
		t.Fatalf("added = %v", rep.Added)
	}
	list, _ := db.ListRecipes()
	if len(list) != 1 || list[0].Title != "Carbonara" {
		t.Fatalf("list = %+v", list)
	}

	// --- restart with no changes: nothing reported ------------------
	rep, err = db.SyncDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Added)+len(rep.Updated)+len(rep.Removed) != 0 {
		t.Fatalf("expected no changes, got %+v", rep)
	}

	// --- change the file: updated, same id --------------------------
	changed := carbonara + "\nServe with extra pecorino.\n"
	write(t, dir, "carbonara.cook", changed)
	before, _ := db.ListRecipes()
	rep, err = db.SyncDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Updated) != 1 || rep.Updated[0] != "carbonara.cook" {
		t.Fatalf("updated = %v", rep.Updated)
	}
	after, _ := db.ListRecipes()
	if after[0].ID != before[0].ID {
		t.Errorf("id changed on update: %d -> %d", before[0].ID, after[0].ID)
	}

	// --- nested directories are scanned ------------------------------
	sub := filepath.Join(dir, "thai")
	os.Mkdir(sub, 0o755)
	write(t, dir, filepath.Join("thai", "pad-thai.cook"),
		">> title: Pad Thai\n\n@rice noodles{200%g}\n\nFry.\n")
	rep, err = db.SyncDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Added) != 1 || rep.Added[0] != "thai/pad-thai.cook" {
		t.Fatalf("added = %v", rep.Added)
	}

	// --- remove a file: recipe removed -------------------------------
	os.Remove(filepath.Join(dir, "carbonara.cook"))
	rep, err = db.SyncDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Removed) != 1 || rep.Removed[0] != "carbonara.cook" {
		t.Fatalf("removed = %v", rep.Removed)
	}
	list, _ = db.ListRecipes()
	if len(list) != 1 || list[0].Title != "Pad Thai" {
		t.Fatalf("list after removal = %+v", list)
	}

	// --- search follows the sync -------------------------------------
	hits, err := db.Search("noodles")
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Title != "Pad Thai" {
		t.Fatalf("hits = %+v", hits)
	}
}

func TestSyncDirMissing(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.SyncDir(filepath.Join(t.TempDir(), "nope")); err != nil {
		t.Fatalf("missing directory should be tolerated, got %v", err)
	}
}
