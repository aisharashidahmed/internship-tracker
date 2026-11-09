// Command invite creates a sign-up link. Sign-up is invite-only.
//
//	go run ./cmd/invite -note "Jordan"                       # one person, valid 14 days
//	go run ./cmd/invite -note "ColorStack" -uses 50 -days 7  # a group link
//
// The link is printed once; only a hash of it is stored, so it can't be
// shown again. Make a new one if it's lost.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/url"
	"os"
	"time"

	"internship-tracker/internal/db"
	"internship-tracker/internal/store"
)

func main() {
	note := flag.String("note", "", "who the invite is for (for your records)")
	uses := flag.Int("uses", 1, "how many accounts the link can create")
	days := flag.Int("days", 14, "days until the link expires")
	flag.Parse()
	if *uses < 1 || *days < 1 {
		log.Fatal("-uses and -days must be at least 1")
	}

	ctx := context.Background()
	pool, err := db.Connect(ctx, db.URLFromEnv())
	if err != nil {
		log.Fatal(err)
	}
	defer pool.Close()

	token, err := store.New(pool).CreateInvite(ctx, *note, *uses, time.Duration(*days)*24*time.Hour)
	if err != nil {
		log.Fatal(err)
	}

	base := os.Getenv("APP_URL")
	if base == "" {
		base = "http://localhost:8080"
	}
	fmt.Printf("%s/signup?invite=%s\n", base, url.QueryEscape(token))
	fmt.Printf("  for %q: %d sign-up(s), expires in %d days\n", *note, *uses, *days)
}
