// Package api implements the HTTP handlers for the recipe backend.
package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"T1MC00K/internal/parser"
	"T1MC00K/internal/store"
)

// Server holds shared dependencies for all handlers.
type Server struct {
	store  *store.Store
	static fs.FS // embedded frontend files (web/)
}

// New creates a Server serving the given store and static file tree.
func New(st *store.Store, static fs.FS) *Server {
	return &Server{store: st, static: static}
}

// Routes builds the full HTTP handler with method-based routing.
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("POST /api/recipes", s.handleUpload)
	mux.HandleFunc("GET /api/recipes", s.handleList)
	mux.HandleFunc("GET /api/recipes/{id}", s.handleGet)
	mux.HandleFunc("DELETE /api/recipes/{id}", s.handleDelete)
	mux.HandleFunc("GET /api/search", s.handleSearch)

	// Single static page at /.
	mux.Handle("GET /{$}", http.FileServerFS(s.static))

	return logRequests(mux)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "healthy"})
}

// handleUpload accepts new recipes as .cook source. Two content types are
// supported:
//
//	multipart/form-data  with one or more "file" fields (browser uploads)
//	text/plain, */body   containing a single raw .cook document
//
// Parsed ingredients, title and cuisine tags are stored and indexed.
func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	type result struct {
		Filename string        `json:"filename"`
		Recipe   *store.Recipe `json:"recipe"`
		Error    string        `json:"error,omitempty"`
	}
	var results []result
	var hadError bool

	upload := func(filename, src string) {
		title := fallbackTitle(filename)
		parsed, err := parser.Parse(src, title)
		if err != nil {
			results = append(results, result{Filename: filename, Error: err.Error()})
			hadError = true
			return
		}
		id, err := s.store.SaveRecipe(parsed)
		if err != nil {
			log.Printf("save recipe %q: %v", filename, err)
			results = append(results, result{Filename: filename, Error: "failed to save recipe"})
			hadError = true
			return
		}
		saved, err := s.store.GetRecipe(id)
		if err != nil {
			log.Printf("reload recipe %d: %v", id, err)
			results = append(results, result{Filename: filename, Error: "saved but could not reload"})
			hadError = true
			return
		}
		results = append(results, result{Filename: filename, Recipe: saved})
	}

	mediatype, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	switch {
	case strings.HasPrefix(mediatype, "multipart/"):
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			httpError(w, http.StatusBadRequest, "invalid multipart form: %v", err)
			return
		}
		files := r.MultipartForm.File["file"]
		if len(files) == 0 {
			httpError(w, http.StatusBadRequest, `no "file" fields in multipart form`)
			return
		}
		for _, fh := range files {
			f, err := fh.Open()
			if err != nil {
				httpError(w, http.StatusBadRequest, "cannot open uploaded file %q", fh.Filename)
				return
			}
			src, _ := io.ReadAll(f)
			f.Close()
			upload(fh.Filename, string(src))
		}
	default:
		body, err := io.ReadAll(io.LimitReader(r.Body, 32<<20))
		if err != nil {
			httpError(w, http.StatusBadRequest, "cannot read body: %v", err)
			return
		}
		upload("", string(body))
	}

	if len(results) == 0 {
		httpError(w, http.StatusBadRequest, "no recipes to upload")
		return
	}

	status := http.StatusCreated
	if hadError && len(results) == 1 {
		// A single failed upload is a client error.
		httpError(w, http.StatusBadRequest, "upload failed: %s", results[0].Error)
		return
	}
	if hadError {
		status = http.StatusMultiStatus
	}
	writeJSON(w, status, results)
}

func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	recipes, err := s.store.ListRecipes()
	if err != nil {
		log.Printf("list recipes: %v", err)
		httpError(w, http.StatusInternalServerError, "failed to list recipes")
		return
	}
	writeJSON(w, http.StatusOK, recipes)
}

func (s *Server) handleGet(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	recipe, err := s.store.GetRecipe(id)
	if errors.Is(err, store.ErrNotFound) {
		httpError(w, http.StatusNotFound, "recipe %d not found", id)
		return
	} else if err != nil {
		log.Printf("get recipe %d: %v", id, err)
		httpError(w, http.StatusInternalServerError, "failed to load recipe")
		return
	}
	writeJSON(w, http.StatusOK, recipe)
}

func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	err := s.store.DeleteRecipe(id)
	if errors.Is(err, store.ErrNotFound) {
		httpError(w, http.StatusNotFound, "recipe %d not found", id)
		return
	} else if err != nil {
		log.Printf("delete recipe %d: %v", id, err)
		httpError(w, http.StatusInternalServerError, "failed to delete recipe")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if query == "" {
		httpError(w, http.StatusBadRequest, "missing q parameter")
		return
	}
	hits, err := s.store.Search(query)
	if err != nil {
		log.Printf("search %q: %v", query, err)
		httpError(w, http.StatusInternalServerError, "search failed")
		return
	}
	if hits == nil {
		hits = []store.RecipeSummary{}
	}
	writeJSON(w, http.StatusOK, hits)
}

// pathID extracts the {id} route value as an int64.
func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		httpError(w, http.StatusBadRequest, "invalid recipe id")
		return 0, false
	}
	return id, true
}

// fallbackTitle derives a title from an uploaded filename ("pad-thai.cook"
// -> "Pad Thai").
func fallbackTitle(filename string) string {
	name := filename
	if i := strings.LastIndexByte(name, '.'); i > 0 {
		name = name[:i]
	}
	name = strings.NewReplacer("-", " ", "_", " ").Replace(name)
	if name == "" {
		return ""
	}
	return strings.ToUpper(name[:1]) + name[1:]
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("write response: %v", err)
	}
}

func httpError(w http.ResponseWriter, status int, format string, args ...any) {
	writeJSON(w, status, map[string]string{"error": fmt.Sprintf(format, args...)})
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Printf("%s %s", r.Method, r.URL.RequestURI())
		next.ServeHTTP(w, r)
	})
}
