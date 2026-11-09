// Command server serves the feed at http://localhost:8080 (or $PORT) and
// ingests every source right away, then every hour.
//
//	go run ./cmd/server                    # feed + hourly ingestion
//	go run ./cmd/server -ingest-every=0    # feed only
package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"internship-tracker/internal/db"
	"internship-tracker/internal/ingest"
	"internship-tracker/internal/source"
	"internship-tracker/internal/store"
	"internship-tracker/internal/web"
)

func main() {
	every := flag.Duration("ingest-every", time.Hour, "how often to ingest every source (0 turns it off)")
	flag.Parse()

	// ctx is cancelled on Ctrl-C (or a stop signal), which stops both the
	// scheduler and the web server cleanly.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := db.Connect(ctx, db.URLFromEnv())
	if err != nil {
		log.Fatal(err)
	}
	defer pool.Close()
	st := store.New(pool)

	if *every > 0 {
		in := ingest.New(st)
		go ingest.Every(ctx, *every, func(ctx context.Context) {
			ingest.LogResults(in.RunAll(ctx, source.All(), ingest.Options{}))
			if n, err := st.DeleteExpiredSessions(ctx); err != nil {
				log.Printf("delete expired sessions: %v", err)
			} else if n > 0 {
				log.Printf("deleted %d expired session(s)", n)
			}
		})
		log.Printf("ingesting now, then every %s", *every)
	}

	// COOKIE_SECURE=1 in production (HTTPS); plain http://localhost needs it off.
	// APP_TIMEZONE: the zone for logged-out visitors (users get their own).
	s, err := web.New(st, st, st, web.Options{
		SecureCookies:   os.Getenv("COOKIE_SECURE") == "1",
		DefaultTimezone: os.Getenv("APP_TIMEZONE"),
	})
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

	go func() {
		<-ctx.Done()
		// Give in-flight page loads a few seconds to finish.
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutdownCtx)
	}()

	log.Printf("feed at http://%s", addr)
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
	log.Print("stopped")
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
