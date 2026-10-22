// T1MC00K is a standalone recipe app: a directory of CookLang (.cook)
// files in, a searchable, nicely rendered library out. The directory is
// the source of truth — the app syncs it into SQLite on every start and
// re-syncs periodically (daily by default).
package main

import (
	"context"
	"embed"
	"errors"
	"flag"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"T1MC00K/internal/api"
	"T1MC00K/internal/store"
)

//go:embed web
var webFS embed.FS

func main() {
	var (
		addr   = flag.String("addr", envOr("T1MC00K_ADDR", ":8090"), "listen address")
		db     = flag.String("db", envOr("T1MC00K_DB", "recipes.db"), "path to the SQLite database")
		dir    = flag.String("dir", envOr("T1MC00K_DIR", "recipes"), "directory of .cook source files")
		resync = flag.Duration("resync", envDurationOr("T1MC00K_RESYNC", 24*time.Hour), "how often to re-sync the recipes directory (0 = only on start)")
		base   = flag.String("base", envOr("T1MC00K_BASE", ""), `URL prefix to mount under, e.g. "/recipes" (empty = root)`)
	)
	flag.Parse()

	st, err := store.Open(*db)
	if err != nil {
		log.Fatalf("open store: %v", err)
	}
	defer st.Close()

	// Pull in the .cook sources: new files are imported, changed files
	// re-parsed, vanished files removed.
	syncAndLog(st, *dir)

	// Re-sync periodically so file edits show up without a restart.
	done := make(chan struct{})
	if *resync > 0 {
		go func() {
			ticker := time.NewTicker(*resync)
			defer ticker.Stop()
			log.Printf("resyncing %s every %s", *dir, *resync)
			for {
				select {
				case <-ticker.C:
					syncAndLog(st, *dir)
				case <-done:
					return
				}
			}
		}()
	}

	static, err := fs.Sub(webFS, "web")
	if err != nil {
		log.Fatalf("embed web: %v", err)
	}

	handler := mount(api.New(st, static).Routes(), *base)

	srv := &http.Server{
		Addr:              *addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		log.Printf("T1MC00K listening on %s (db: %s, base: %q)", *addr, *db, *base)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server error: %v", err)
		}
	}()

	// Graceful shutdown on Ctrl-C / SIGTERM.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	close(done) // stop the resync loop

	log.Println("shutting down...")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("shutdown: %v", err)
	}
	log.Println("server closed")
}

// syncAndLog runs one directory sync and reports what changed.
func syncAndLog(st *store.Store, dir string) {
	rep, err := st.SyncDir(dir)
	if err != nil {
		log.Printf("sync %s: %v", dir, err)
		return
	}
	if len(rep.Added)+len(rep.Updated)+len(rep.Removed) == 0 {
		return // quiet when nothing changed
	}
	log.Printf("synced %s: %d added, %d updated, %d removed",
		dir, len(rep.Added), len(rep.Updated), len(rep.Removed))
	for _, f := range rep.Added {
		log.Printf("  + %s", f)
	}
	for _, f := range rep.Updated {
		log.Printf("  ~ %s", f)
	}
	for _, f := range rep.Removed {
		log.Printf("  - %s", f)
	}
}

// mount makes the whole app reachable under a URL prefix. An empty or "/"
// prefix returns the handler unchanged. Requests to the bare prefix are
// redirected to prefix/ so relative URLs in the frontend resolve correctly.
func mount(h http.Handler, prefix string) http.Handler {
	prefix = "/" + strings.Trim(prefix, "/")
	if prefix == "/" {
		return h
	}
	root := http.NewServeMux()
	root.Handle(prefix+"/", http.StripPrefix(prefix, h))
	root.HandleFunc(prefix, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, prefix+"/", http.StatusPermanentRedirect)
	})
	return root
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envDurationOr(key string, fallback time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
		log.Printf("invalid %s=%q, using %s", key, v, fallback)
	}
	return fallback
}
