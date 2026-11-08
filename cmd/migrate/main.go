// Command migrate creates or updates the database schema.
//
//	go run ./cmd/migrate
package main

import (
	"context"
	"fmt"
	"log"

	"internship-tracker/internal/db"
)

func main() {
	ctx := context.Background()
	pool, err := db.Connect(ctx, db.URLFromEnv())
	if err != nil {
		log.Fatal(err)
	}
	defer pool.Close()

	if err := db.Migrate(ctx, pool); err != nil {
		log.Fatal(err)
	}
	fmt.Println("schema is up to date")
}
