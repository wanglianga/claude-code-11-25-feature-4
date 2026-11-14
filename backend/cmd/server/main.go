package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"recycletrace/backend/internal/app"
	"recycletrace/backend/internal/db"
	"recycletrace/backend/internal/httpapi"
)

func defaultDSN() string {
	if v := os.Getenv("DATABASE_URL"); v != "" {
		return v
	}
	host := getenv("PGHOST", "127.0.0.1")
	port := getenv("PGPORT", "5432")
	user := getenv("PGUSER", "ccdev")
	pass := getenv("PGPASSWORD", "ccdev")
	name := getenv("PGDATABASE", "ccdev")
	return "postgres://" + user + ":" + pass + "@" + host + ":" + port + "/" + name + "?sslmode=disable"
}

func getenv(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func main() {
	addr := flag.String("addr", getenv("ADDR", ":8080"), "listen address")
	webDir := flag.String("web", getenv("WEB_DIR", "web/dist"), "static frontend directory")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	dsn := defaultDSN()
	d, err := db.Open(ctx, dsn)
	if err != nil {
		log.Fatalf("database: %v", err)
	}
	defer d.Close()
	if err := d.Migrate(ctx); err != nil {
		log.Fatalf("migrate: %v", err)
	}
	a := app.New(d)
	// DEMO_SEED=1 seeds the demo story only when the database has no trips, so a
	// restart never wipes real work.
	if os.Getenv("DEMO_SEED") == "1" {
		var trips int
		if err := d.Pool.QueryRow(ctx, `select count(*) from rt.trips`).Scan(&trips); err != nil {
			log.Fatalf("seed check: %v", err)
		}
		if trips == 0 {
			if _, err := a.ResetDemo(ctx); err != nil {
				log.Fatalf("seed reset: %v", err)
			}
			if err := a.SeedDemo(ctx); err != nil {
				log.Fatalf("seed: %v", err)
			}
			log.Printf("demo data seeded")
		}
	}

	// Resolve the static directory from the flag, then fall back to a few
	// conventional locations so the binary works regardless of cwd.
	var static http.Handler
	candidates := []string{*webDir, "web/dist", "backend/web/dist", "/app/web/dist"}
	exeDir := ""
	if exe, err := os.Executable(); err == nil {
		exeDir = filepath.Dir(exe)
		candidates = append(candidates, filepath.Join(exeDir, "web", "dist"))
	}
	for _, c := range candidates {
		abs, err := filepath.Abs(c)
		if err != nil {
			continue
		}
		if st, err := os.Stat(abs); err == nil && st.IsDir() {
			if _, err := os.Stat(filepath.Join(abs, "index.html")); err == nil {
				static = spaHandler(abs)
				log.Printf("serving frontend from %s", abs)
				break
			}
		}
	}
	if static == nil {
		log.Printf("WARNING: frontend dist not found (looked in %v); API only", candidates)
	}
	srv := httpapi.New(a, static)
	server := &http.Server{
		Addr:              *addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      120 * time.Second,
	}
	log.Printf("listening on %s", *addr)
	if err := server.ListenAndServe(); err != nil && !strings.Contains(err.Error(), "Server closed") {
		log.Fatal(err)
	}
}

// spaHandler serves files and falls back to index.html for client-side routes.
func spaHandler(root string) http.Handler {
	fs := http.Dir(root)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := filepath.Clean(r.URL.Path)
		if p == "/" {
			http.ServeFile(w, r, filepath.Join(root, "index.html"))
			return
		}
		if st, err := os.Stat(filepath.Join(root, p)); err == nil && !st.IsDir() {
			http.ServeFile(w, r, filepath.Join(root, p))
			return
		}
		http.ServeFile(w, r, filepath.Join(root, "index.html"))
		_ = fs
	})
}
