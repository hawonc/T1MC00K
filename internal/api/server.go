// Package api implements the HTTP handlers for the recipe backend.
package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"strconv"
	"strings"

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
	mux.HandleFunc("GET /api/recipes", s.handleList)
	mux.HandleFunc("GET /api/recipes/{id}", s.handleGet)
	mux.HandleFunc("GET /api/search", s.handleSearch)

	// Single static page at /.
	mux.Handle("GET /{$}", http.FileServerFS(s.static))

	return logRequests(mux)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "healthy"})
}

func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	recipes, err := s.store.ListRecipes()
	if err != nil {
		log.Printf("list recipes: %v", err)
		httpError(w, http.StatusInternalServerError, "failed to list recipes")
		return
	}
	if recipes == nil {
		recipes = []store.RecipeSummary{} // empty JSON array, not null
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
