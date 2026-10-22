package store

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"T1MC00K/internal/parser"
)

// SyncReport summarises what one SyncDir pass changed.
type SyncReport struct {
	Added   []string // file paths newly imported
	Updated []string // file paths whose content changed
	Removed []string // recipes whose source file disappeared
}

// SyncDir makes the database reflect the .cook files under dir (recursively).
// The directory is the source of truth: new files are imported, changed
// files are re-parsed, and recipes whose files are gone (or legacy rows
// with no file at all) are deleted.
func (s *Store) SyncDir(dir string) (*SyncReport, error) {
	files, err := collectCookFiles(dir)
	if err != nil {
		return nil, err
	}

	rep := &SyncReport{}
	hashes := map[string]string{} // relative path -> content hash

	for _, rel := range files {
		data, err := os.ReadFile(filepath.Join(dir, rel))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", rel, err)
		}
		sum := sha256.Sum256(data)
		hash := hex.EncodeToString(sum[:])
		hashes[rel] = hash

		var stored string
		err = s.db.QueryRow(`SELECT hash FROM recipes WHERE path = ?`, rel).Scan(&stored)
		switch {
		case err == nil && stored == hash:
			continue // unchanged
		case err != nil:
			if !isNoRows(err) {
				return nil, err
			}
			rep.Added = append(rep.Added, rel)
		default:
			rep.Updated = append(rep.Updated, rel)
		}

		r, err := parser.Parse(string(data), titleFromFilename(rel))
		if err != nil {
			// A bad file shouldn't take the whole app down; skip it.
			fmt.Fprintf(os.Stderr, "t1mc00k: skipping %s: %v\n", rel, err)
			delete(hashes, rel)
			continue
		}
		if _, err := s.upsertRecipe(rel, hash, r); err != nil {
			return nil, fmt.Errorf("store %s: %w", rel, err)
		}
	}

	// Remove recipes whose files are gone (and pre-sync rows with no path).
	rows, err := s.db.Query(`SELECT id, path FROM recipes`)
	if err != nil {
		return nil, err
	}
	type row struct {
		id   int64
		path string
	}
	var all []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.path); err != nil {
			rows.Close()
			return nil, err
		}
		all = append(all, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, r := range all {
		if hash, ok := hashes[r.path]; ok && hash != "" {
			continue // still present
		}
		if err := s.deleteByID(r.id); err != nil {
			return nil, err
		}
		if r.path != "" {
			rep.Removed = append(rep.Removed, r.path)
		}
	}

	return rep, nil
}

// collectCookFiles returns the relative paths of all .cook files under dir,
// sorted for stable recipe ids across restarts.
func collectCookFiles(dir string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if strings.EqualFold(filepath.Ext(path), ".cook") {
			rel, err := filepath.Rel(dir, path)
			if err != nil {
				return err
			}
			files = append(files, filepath.ToSlash(rel))
		}
		return nil
	})
	if os.IsNotExist(err) {
		return nil, nil // no recipes directory yet: nothing to sync
	}
	sort.Strings(files)
	return files, err
}

// titleFromFilename derives a fallback title from a path
// ("thai/pad-thai.cook" -> "Pad thai").
func titleFromFilename(path string) string {
	base := filepath.Base(path)
	if i := strings.LastIndexByte(base, '.'); i > 0 {
		base = base[:i]
	}
	base = strings.NewReplacer("-", " ", "_", " ").Replace(base)
	if base == "" {
		return ""
	}
	return strings.ToUpper(base[:1]) + base[1:]
}
