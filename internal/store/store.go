// Package store provides SQLite-backed persistence for recipes, including
// an FTS5 full-text index over titles, ingredients and method text.
package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"T1MC00K/internal/parser"
)

// ErrNotFound is returned by Get/Delete when no recipe has the given id.
var ErrNotFound = errors.New("recipe not found")

// Recipe is a stored recipe, as returned by the API.
type Recipe struct {
	ID          int64               `json:"id"`
	Title       string              `json:"title"`
	Tags        []string            `json:"tags"`
	Times       parser.Times        `json:"times"`
	Servings    string              `json:"servings,omitempty"`
	Provenance  string              `json:"provenance,omitempty"`
	Ingredients []parser.Ingredient `json:"ingredients"`
	Steps       []string            `json:"steps"`
	Source      string              `json:"source"`
	CreatedAt   time.Time           `json:"created_at"`
	UpdatedAt   time.Time           `json:"updated_at"`
}

// RecipeSummary is the lightweight form used in list/search results.
type RecipeSummary struct {
	ID        int64    `json:"id"`
	Title     string   `json:"title"`
	Tags      []string `json:"tags"`
	CreatedAt string   `json:"created_at,omitempty"`
	Snippet   string   `json:"snippet,omitempty"` // populated for search results only
}

// Store wraps the SQLite database.
type Store struct {
	db *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS recipes (
	id          INTEGER PRIMARY KEY,
	title       TEXT NOT NULL,
	tags        TEXT NOT NULL DEFAULT '[]',      -- JSON array of tag strings
	times       TEXT NOT NULL DEFAULT '{}',      -- JSON: {total, prep, cook}
	servings    TEXT NOT NULL DEFAULT '',
	provenance  TEXT NOT NULL DEFAULT '',        -- ">> source:" (book, family, URL…)
	ingredients TEXT NOT NULL DEFAULT '[]',      -- JSON array of parser.Ingredient
	steps       TEXT NOT NULL DEFAULT '[]',      -- JSON array of step strings
	source      TEXT NOT NULL,
	created_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
	updated_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE TABLE IF NOT EXISTS tags (
	id   INTEGER PRIMARY KEY,
	name TEXT NOT NULL UNIQUE
);

CREATE TABLE IF NOT EXISTS recipe_tags (
	recipe_id INTEGER NOT NULL REFERENCES recipes(id) ON DELETE CASCADE,
	tag_id    INTEGER NOT NULL REFERENCES tags(id)  ON DELETE CASCADE,
	PRIMARY KEY (recipe_id, tag_id)
);

CREATE INDEX IF NOT EXISTS idx_tags_name ON tags(name);

-- Full-text search index. rowid is kept in sync with recipes.id so we can
-- join straight back to recipe metadata.
CREATE VIRTUAL TABLE IF NOT EXISTS recipes_fts USING fts5(
	title,
	ingredients,
	steps,
	tokenize = 'porter unicode61'
);
`

// Open opens (creating if necessary) the database at path and ensures the
// schema exists.
func Open(path string) (*Store, error) {
	// _pragma is modernc.org/sqlite's DSN syntax for PRAGMA statements.
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	db.SetMaxOpenConns(1) // sqlite is happiest with a single writer

	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return &Store{db: db}, nil
}

// migrate adds columns introduced after a database was first created, so
// older dev databases keep working without a manual rebuild.
func migrate(db *sql.DB) error {
	add := []struct{ col, ddl string }{
		{"times", `ALTER TABLE recipes ADD COLUMN times TEXT NOT NULL DEFAULT '{}'`},
		{"servings", `ALTER TABLE recipes ADD COLUMN servings TEXT NOT NULL DEFAULT ''`},
		{"provenance", `ALTER TABLE recipes ADD COLUMN provenance TEXT NOT NULL DEFAULT ''`},
	}
	for _, m := range add {
		var name string
		err := db.QueryRow(`SELECT name FROM pragma_table_info('recipes') WHERE name = ?`, m.col).Scan(&name)
		if errors.Is(err, sql.ErrNoRows) {
			if _, err := db.Exec(m.ddl); err != nil {
				return fmt.Errorf("add column %s: %w", m.col, err)
			}
		} else if err != nil {
			return err
		}
	}
	return nil
}

// Close closes the underlying database.
func (s *Store) Close() error { return s.db.Close() }

// SaveRecipe stores a parsed recipe (insert or update) and refreshes the
// search index and tag tables.
func (s *Store) SaveRecipe(r *parser.Recipe) (int64, error) {
	tagsJSON, _ := json.Marshal(r.Tags)
	timesJSON, _ := json.Marshal(r.Times)
	ingJSON, _ := json.Marshal(r.Ingredients)
	stepsJSON, _ := json.Marshal(r.Steps)

	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	res, err := tx.Exec(`
		INSERT INTO recipes (title, tags, times, servings, provenance, ingredients, steps, source)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		r.Title, string(tagsJSON), string(timesJSON), r.Servings, r.Provenance, string(ingJSON), string(stepsJSON), r.Source)
	if err != nil {
		return 0, fmt.Errorf("insert recipe: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}

	if err := indexTags(tx, id, r.Tags); err != nil {
		return 0, err
	}
	if err := indexFTS(tx, id, r); err != nil {
		return 0, err
	}

	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return id, nil
}

// GetRecipe returns the full recipe with the given id.
func (s *Store) GetRecipe(id int64) (*Recipe, error) {
	row := s.db.QueryRow(`SELECT id, title, tags, times, servings, provenance, ingredients, steps, source, created_at, updated_at
		FROM recipes WHERE id = ?`, id)
	r, err := scanRecipe(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return r, err
}

// ListRecipes returns all recipes, newest first.
func (s *Store) ListRecipes() ([]RecipeSummary, error) {
	rows, err := s.db.Query(`SELECT id, title, tags, created_at FROM recipes ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []RecipeSummary
	for rows.Next() {
		var sum RecipeSummary
		var tagsJSON, created string
		if err := rows.Scan(&sum.ID, &sum.Title, &tagsJSON, &created); err != nil {
			return nil, err
		}
		json.Unmarshal([]byte(tagsJSON), &sum.Tags)
		sum.CreatedAt = created
		out = append(out, sum)
	}
	return out, rows.Err()
}

// DeleteRecipe removes a recipe, its tag links and its search entries.
func (s *Store) DeleteRecipe(id int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	res, err := tx.Exec(`DELETE FROM recipes WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	if _, err := tx.Exec(`DELETE FROM recipes_fts WHERE rowid = ?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// Search runs a full-text query against titles, ingredients and steps,
// ranked by relevance (FTS5 bm25), and returns matching recipe summaries
// with a snippet of the best-matching text.
func (s *Store) Search(query string) ([]RecipeSummary, error) {
	match, err := sanitizeFTSQuery(query)
	if err != nil {
		return nil, err
	}

	// snippet() is per-column, so we pull one from each column with
	// detectable markers (\x01/\x02) and prefer the one that actually
	// contains a match, stripping the markers afterwards.
	const m1, m2 = "\x01", "\x02"
	rows, err := s.db.Query(`
		SELECT r.id, r.title, r.tags, r.created_at,
		       snippet(recipes_fts, 1, ?, ?, ' … ', 14),
		       snippet(recipes_fts, 2, ?, ?, ' … ', 14),
		       snippet(recipes_fts, 0, ?, ?, ' … ', 14)
		FROM recipes_fts f
		JOIN recipes r ON r.id = f.rowid
		WHERE recipes_fts MATCH ?
		ORDER BY rank
		LIMIT 100`, m1, m2, m1, m2, m1, m2, match)
	if err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}
	defer rows.Close()

	var out []RecipeSummary
	for rows.Next() {
		var sum RecipeSummary
		var tagsJSON, created, ingSnip, stepSnip, titleSnip string
		if err := rows.Scan(&sum.ID, &sum.Title, &tagsJSON, &created, &ingSnip, &stepSnip, &titleSnip); err != nil {
			return nil, err
		}
		json.Unmarshal([]byte(tagsJSON), &sum.Tags)
		sum.CreatedAt = created

		strip := func(s string) string {
			s = strings.ReplaceAll(s, m1, "")
			return strings.ReplaceAll(s, m2, "")
		}
		// Preference: the snippet that contains an actual match, with
		// ingredients (the primary search target) winning over steps and title.
		switch {
		case strings.Contains(ingSnip, m1):
			sum.Snippet = strip(ingSnip)
		case strings.Contains(stepSnip, m1):
			sum.Snippet = strip(stepSnip)
		case strings.Contains(titleSnip, m1):
			sum.Snippet = strip(titleSnip)
		default: // matched via a different column combination; show ingredients
			sum.Snippet = strings.TrimSpace(strip(ingSnip))
		}
		out = append(out, sum)
	}
	return out, rows.Err()
}

// sanitizeFTSQuery converts free user input into a safe FTS5 MATCH
// expression: every whitespace-separated token is double-quoted and the
// tokens are ANDed together. Anything empty is an error.
func sanitizeFTSQuery(query string) (string, error) {
	fields := strings.Fields(query)
	if len(fields) == 0 {
		return "", fmt.Errorf("empty search query")
	}
	var b strings.Builder
	for i, f := range fields {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteByte('"')
		b.WriteString(strings.ReplaceAll(f, `"`, `""`))
		b.WriteByte('"')
	}
	return b.String(), nil
}

// indexTags maintains the normalized tags/recipe_tags tables.
func indexTags(tx *sql.Tx, recipeID int64, tags []string) error {
	if _, err := tx.Exec(`DELETE FROM recipe_tags WHERE recipe_id = ?`, recipeID); err != nil {
		return err
	}
	for _, tag := range tags {
		if _, err := tx.Exec(`INSERT INTO tags (name) VALUES (?) ON CONFLICT(name) DO NOTHING`, tag); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO recipe_tags (recipe_id, tag_id)
			SELECT ?, id FROM tags WHERE name = ?`, recipeID, tag); err != nil {
			return err
		}
	}
	return nil
}

// indexFTS (re)writes the FTS row for a recipe. The FTS table is keyed by
// rowid == recipes.id.
func indexFTS(tx *sql.Tx, id int64, r *parser.Recipe) error {
	if _, err := tx.Exec(`DELETE FROM recipes_fts WHERE rowid = ?`, id); err != nil {
		return err
	}
	var ingText strings.Builder
	for _, ing := range r.Ingredients {
		ingText.WriteString(ing.Name)
		ingText.WriteByte(' ')
	}
	stepsText := strings.Join(r.Steps, "\n")
	_, err := tx.Exec(`INSERT INTO recipes_fts (rowid, title, ingredients, steps) VALUES (?, ?, ?, ?)`,
		id, r.Title, ingText.String(), stepsText)
	return err
}

// rowScanner abstracts *sql.Row and *sql.Rows for scanRecipe.
type rowScanner interface{ Scan(dest ...any) error }

func scanRecipe(scan func(dest ...any) error) (*Recipe, error) {
	var r Recipe
	var tagsJSON, timesJSON, ingJSON, stepsJSON, created, updated string
	if err := scan(&r.ID, &r.Title, &tagsJSON, &timesJSON, &r.Servings, &r.Provenance, &ingJSON, &stepsJSON, &r.Source, &created, &updated); err != nil {
		return nil, err
	}
	json.Unmarshal([]byte(tagsJSON), &r.Tags)
	json.Unmarshal([]byte(timesJSON), &r.Times)
	json.Unmarshal([]byte(ingJSON), &r.Ingredients)
	json.Unmarshal([]byte(stepsJSON), &r.Steps)
	r.CreatedAt = parseTime(created)
	r.UpdatedAt = parseTime(updated)
	return &r, nil
}

// parseTime parses the UTC timestamps produced by the schema's strftime
// defaults (RFC 3339 with milliseconds, e.g. 2026-10-16T03:26:37.413Z).
func parseTime(s string) time.Time {
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t
	}
	return time.Time{}
}
