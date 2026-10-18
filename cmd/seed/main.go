// Command seed initializes a recipe database from .cook files on disk.
//
// It exists for local development: the upload API is disabled pending
// authentication, so this is the way to get recipes into a dev database.
//
//	go run ./cmd/seed -db recipes.db samples/pad-thai.cook
//
// Every invocation INSERTs (it does not deduplicate) — seed a fresh
// database file if you don't want copies.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	"T1MC00K/internal/parser"
	"T1MC00K/internal/store"
)

func main() {
	dbPath := flag.String("db", "recipes.db", "path to the SQLite database to seed")
	flag.Parse()

	files := flag.Args()
	if len(files) == 0 {
		fmt.Fprintln(os.Stderr, "usage: seed [-db recipes.db] file.cook [file2.cook ...]")
		os.Exit(2)
	}

	st, err := store.Open(*dbPath)
	if err != nil {
		log.Fatalf("open store: %v", err)
	}
	defer st.Close()

	for _, path := range files {
		src, err := os.ReadFile(path)
		if err != nil {
			log.Fatalf("read %s: %v", path, err)
		}
		r, err := parser.Parse(string(src), titleFromFilename(path))
		if err != nil {
			log.Fatalf("parse %s: %v", path, err)
		}
		id, err := st.SaveRecipe(r)
		if err != nil {
			log.Fatalf("save %s: %v", path, err)
		}
		fmt.Printf("seeded %-40s -> id %d  %q  %v\n", path, id, r.Title, r.Tags)
	}
}

// titleFromFilename derives a fallback title from a path, mirroring the
// API's behaviour ("samples/pad-thai.cook" -> "Pad thai").
func titleFromFilename(path string) string {
	base := path
	if i := strings.LastIndexByte(base, '/'); i >= 0 {
		base = base[i+1:]
	}
	if i := strings.LastIndexByte(base, '.'); i > 0 {
		base = base[:i]
	}
	base = strings.NewReplacer("-", " ", "_", " ").Replace(base)
	if base == "" {
		return ""
	}
	return strings.ToUpper(base[:1]) + base[1:]
}
