// Command server serves the feed and ingests every source right away, then
// every hour. Locally it listens on localhost:8080. On a host (Render, Fly,
// ...) set PORT and it listens on all interfaces, as the host expects.
//
// Settings come from the environment, so the same binary runs anywhere:
//
//	DATABASE_URL   Postgres connection string
//	PORT           set by the host; listen on :PORT
//	COOKIE_SECURE  1 when served over HTTPS (always, on a host)
//	TRUST_PROXY    1 when behind the host's proxy, to see real visitor IPs
//	APP_TIMEZONE   timezone for logged-out visitors (default America/New_York)
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
	// The timezone database, built into the binary, so every user's
	// timezone works even on a host whose image has no zoneinfo files.
	_ "time/tzdata"

	"internship-tracker/internal/db"
	"internship-tracker/internal/ingest"
	"internship-tracker/internal/linkcheck"
	"internship-tracker/internal/source"
	"internship-tracker/internal/store"
	"internship-tracker/internal/web"
)

func main() {
	every := flag.Duration("ingest-every", time.Hour, "how often to ingest every source (0 turns it off)")
	checkEvery := flag.Duration("check-links-every", time.Hour, "how often to check due apply links (0 turns it off)")
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
	// Bring the schema up to date before serving, so a deploy is just
	// "start the new version". Already-applied migrations are skipped.
	if err := db.Migrate(ctx, pool); err != nil {
		log.Fatal(err)
	}
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

	if *checkEvery > 0 {
		// Each run checks the links that are due (each link once a day),
		// oldest first, at most 400 per run, so the work spreads out.
		checker := &linkcheck.Checker{
			Store:     st,
			Client:    linkcheck.NewClient(),
			UserAgent: linkcheck.Agent + "/1.0 (+https://github.com/aisharashidahmed/internship-tracker)",
			PerHost:   5 * time.Second, // never more than one request every 5 seconds to a website
			Sites:     6,
		}
		go ingest.Every(ctx, *checkEvery, func(ctx context.Context) {
			sum, err := checker.Run(ctx, 400)
			if err != nil && ctx.Err() == nil {
				log.Printf("link checks: %v", err)
			}
			log.Print(sum)
		})
		log.Printf("checking apply links now, then every %s", *checkEvery)
	}

	// COOKIE_SECURE=1 in production (HTTPS); plain http://localhost needs it off.
	// APP_TIMEZONE: the zone for logged-out visitors (users get their own).
	s, err := web.New(st, st, st, st, web.Options{
		SecureCookies:   os.Getenv("COOKIE_SECURE") == "1",
		TrustProxy:      os.Getenv("TRUST_PROXY") == "1",
		DefaultTimezone: os.Getenv("APP_TIMEZONE"),
	})
	if err != nil {
		log.Fatal(err)
	}
	// Locally: only this computer can connect. On a host: PORT is set and
	// we must accept connections from the host's proxy, so listen on all.
	addr := "localhost:8080"
	if port := os.Getenv("PORT"); port != "" {
		addr = ":" + port
	}
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

	log.Printf("listening on %s", addr)
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
	log.Print("stopped")
}
