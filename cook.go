// T1MC00K is a standalone recipe storage app: CookLang (.cook) files in, a
// searchable recipe library out.
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
		addr = flag.String("addr", envOr("T1MC00K_ADDR", ":8090"), "listen address")
		db   = flag.String("db", envOr("T1MC00K_DB", "recipes.db"), "path to the SQLite database")
		base = flag.String("base", envOr("T1MC00K_BASE", ""), `URL prefix to mount under, e.g. "/recipes" (empty = root)`)
	)
	flag.Parse()

	st, err := store.Open(*db)
	if err != nil {
		log.Fatalf("open store: %v", err)
	}
	defer st.Close()

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

	log.Println("shutting down...")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("shutdown: %v", err)
	}
	log.Println("server closed")
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
