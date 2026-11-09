// Command server serves the feed at http://localhost:8080 (or $PORT).
//
//	go run ./cmd/server
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"time"

	"internship-tracker/internal/db"
	"internship-tracker/internal/store"
	"internship-tracker/internal/web"
)

func main() {
	ctx := context.Background()
	pool, err := db.Connect(ctx, db.URLFromEnv())
	if err != nil {
		log.Fatal(err)
	}
	defer pool.Close()

	s, err := web.New(store.New(pool))
	if err != nil {
		log.Fatal(err)
	}

	addr := "localhost:" + envOr("PORT", "8080")
	srv := &http.Server{
		Addr:    addr,
		Handler: s.Handler(),
		// Don't let a slow or stuck client hold a connection open forever.
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      15 * time.Second,
	}
	log.Printf("feed at http://%s", addr)
	log.Fatal(srv.ListenAndServe())
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
