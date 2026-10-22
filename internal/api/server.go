// Package api implements the HTTP layer: server-rendered HTML pages
// (Bear Blog style, no JavaScript) plus a read-only JSON API.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"T1MC00K/internal/store"
)

// Server holds shared dependencies for all handlers.
type Server struct {
	store *store.Store
	tmpl  *templates
}

// templates holds one cloned template set per page so each page can
// override the layout's "content" and "title" blocks independently.
type templates struct {
	home   *template.Template
	recipe *template.Template
	err    *template.Template
}

func parseTemplates(fsys fs.FS) (*templates, error) {
	funcs := template.FuncMap{
		"dateOnly": func(s string) string {
			if len(s) >= 10 {
				return s[:10]
			}
			return s
		},
		"lower": strings.ToLower,
		"allTags": func(recipes []store.RecipeSummary) []string {
			seen := map[string]bool{}
			var out []string
			for _, r := range recipes {
				for _, t := range r.Tags {
					if !seen[t] {
						seen[t] = true
						out = append(out, t)
					}
				}
			}
			sort.Strings(out)
			return out
		},
	}
	clone := func(page string) (*template.Template, error) {
		base, err := template.New("layout").Funcs(funcs).ParseFS(fsys, "layout.html")
		if err != nil {
			return nil, err
		}
		return base.ParseFS(fsys, page)
	}
	home, err := clone("home.html")
	if err != nil {
		return nil, err
	}
	recipe, err := clone("recipe.html")
	if err != nil {
		return nil, err
	}
	errPage, err := clone("error.html")
	if err != nil {
		return nil, err
	}
	return &templates{home: home, recipe: recipe, err: errPage}, nil
}

// New creates a Server serving the given store, rendering the HTML
// templates from tfs.
func New(st *store.Store, tfs fs.FS) (*Server, error) {
	tmpl, err := parseTemplates(tfs)
	if err != nil {
		return nil, fmt.Errorf("parse templates: %w", err)
	}
	return &Server{store: st, tmpl: tmpl}, nil
}

// Routes builds the full HTTP handler with method-based routing.
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()

	// HTML pages
	mux.HandleFunc("GET /{$}", s.handleHome)
	mux.HandleFunc("GET /recipes/{id}", s.handleRecipePage)

	// JSON API
	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("GET /api/recipes", s.handleList)
	mux.HandleFunc("GET /api/recipes/{id}", s.handleGet)
	mux.HandleFunc("GET /api/search", s.handleSearch)

	return secureHeaders(logRequests(mux))
}

// secureHeaders locks the pages down: there is no JavaScript and no
// external anything, so almost everything can be denied outright.
func secureHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy",
			"default-src 'none'; style-src 'unsafe-inline'; form-action 'self'")
		w.Header().Set("Referrer-Policy", "no-referrer-when-downgrade")
		next.ServeHTTP(w, r)
	})
}

// ---- HTML pages ---------------------------------------------------------

type homeData struct {
	Base    string
	Query   string
	Recipes []store.RecipeSummary
}

func (s *Server) handleHome(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))

	var recipes []store.RecipeSummary
	var err error
	if query != "" {
		recipes, err = s.store.Search(query)
	} else {
		recipes, err = s.store.ListRecipes()
	}
	if err != nil {
		log.Printf("home: %v", err)
		s.renderError(w, http.StatusInternalServerError, "Something went wrong", "failed to load recipes")
		return
	}
	if recipes == nil {
		recipes = []store.RecipeSummary{}
	}
	s.render(w, http.StatusOK, s.tmpl.home, homeData{Base: basePrefix(r), Query: query, Recipes: recipes})
}

// bylinePart is one segment of the italic subtext under a recipe title.
type bylinePart struct {
	Text string
	URL  string // non-empty renders as a link
}

// methodStep is one numbered instruction inside a run.
type methodStep struct {
	Text  string
	Notes []string
}

// methodRun is a stretch of the method: either an ordered list of steps
// or a single prose paragraph, optionally preceded by a section header.
type methodRun struct {
	Section string
	Prose   bool
	Text    string
	Notes   []string
	Steps   []methodStep
}

type recipeData struct {
	Base   string
	Recipe *store.Recipe
	Byline []bylinePart
	Method []methodRun
}

func (s *Server) handleRecipePage(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		s.renderError(w, http.StatusBadRequest, "Invalid recipe id", "recipe ids are numbers")
		return
	}
	recipe, err := s.store.GetRecipe(id)
	if errors.Is(err, store.ErrNotFound) {
		s.renderError(w, http.StatusNotFound, "Not found", fmt.Sprintf("there is no recipe %d", id))
		return
	} else if err != nil {
		log.Printf("get recipe %d: %v", id, err)
		s.renderError(w, http.StatusInternalServerError, "Something went wrong", "failed to load recipe")
		return
	}
	s.render(w, http.StatusOK, s.tmpl.recipe, recipeData{
		Base:   basePrefix(r),
		Recipe: recipe,
		Byline: buildByline(recipe),
		Method: methodRuns(recipe.Steps),
	})
}

// buildByline assembles the "prep 10 mins · serves 4 · from …" subtext.
func buildByline(r *store.Recipe) []bylinePart {
	var parts []bylinePart
	if r.Times.Prep != "" {
		parts = append(parts, bylinePart{Text: "prep " + r.Times.Prep})
	}
	if r.Times.Cook != "" {
		parts = append(parts, bylinePart{Text: "cook " + r.Times.Cook})
	}
	if r.Times.Total != "" {
		parts = append(parts, bylinePart{Text: r.Times.Total})
	}
	if r.Servings != "" {
		parts = append(parts, bylinePart{Text: "serves " + r.Servings})
	}
	if p := r.Provenance; p != "" {
		if u, err := url.Parse(p); err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" {
			parts = append(parts, bylinePart{Text: "from " + u.Hostname(), URL: u.String()})
		} else {
			parts = append(parts, bylinePart{Text: "from " + p})
		}
	}
	return parts
}

// methodRuns flattens structured steps into renderable runs: consecutive
// ordered steps share an <ol>; prose paragraphs and section headers start
// new runs.
func methodRuns(steps store.Steps) []methodRun {
	var runs []methodRun
	for _, st := range steps {
		section := st.Section // set on the first step after a header
		if st.TextBlock {
			runs = append(runs, methodRun{Section: section, Prose: true, Text: st.Text, Notes: st.Notes})
			continue
		}
		if n := len(runs); n > 0 && !runs[n-1].Prose && (section == "" || runs[n-1].Section == section) {
			runs[n-1].Steps = append(runs[n-1].Steps, methodStep{Text: st.Text, Notes: st.Notes})
		} else {
			runs = append(runs, methodRun{Section: section, Steps: []methodStep{{Text: st.Text, Notes: st.Notes}}})
		}
	}
	return runs
}

// basePrefix extracts the app's mount prefix from the request context
// (set by WithBase, from the -base flag in main) so links work under any
// mount point.
type baseCtxKey struct{}

// WithBase returns a handler that records the app's mount prefix in the
// request context, where templates pick it up for link generation.
func WithBase(base string, h http.Handler) http.Handler {
	base = "/" + strings.Trim(base, "/")
	if base == "/" {
		base = ""
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), baseCtxKey{}, base)))
	})
}

func basePrefix(r *http.Request) string {
	if v, ok := r.Context().Value(baseCtxKey{}).(string); ok {
		return v
	}
	return ""
}

func (s *Server) render(w http.ResponseWriter, status int, t *template.Template, data any) {
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		log.Printf("render %s: %v", t.Name(), err)
		http.Error(w, "template error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	buf.WriteTo(w)
}

type errorData struct {
	Base    string
	Title   string
	Message string
}

func (s *Server) renderError(w http.ResponseWriter, status int, title, message string) {
	s.render(w, status, s.tmpl.err, errorData{Base: "", Title: title, Message: message})
}

// ---- JSON API -----------------------------------------------------------

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
