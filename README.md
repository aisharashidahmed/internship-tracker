# internship-tracker (name TBD)

Pulls internship listings from public GitHub repos, merges duplicates, and
(soon) tracks your applications. Spec: `Internship Tracker — Project Spec.pdf`.

## Run it

Needs Go, and Postgres running locally (`brew services start postgresql@18`).

```sh
createdb internship_tracker          # once
createdb internship_tracker_test     # once, for tests
go run ./cmd/migrate                 # create/update tables
go run ./cmd/ingest                  # fetch Simplify and save listings
go run ./cmd/ingest -force           # ignore the ETag and re-save everything
go test ./...                        # all tests (store tests use the _test DB)
```

Set `DATABASE_URL` / `TEST_DATABASE_URL` to use other databases.

## How the code is laid out

Data flows one way: **source file → postings → listings → database**.

| Path | Job | Knows about |
|---|---|---|
| `internal/listing` | Common `Posting`/`Listing` types, name cleanup, `DedupeKey`, `Group` | nothing else |
| `internal/source` | The `Adapter` interface | `listing` |
| `internal/source/simplify` | Parse Simplify's `listings.json` | `listing` |
| `internal/store` | All SQL: upsert companies, listings, postings | `listing` |
| `internal/ingest` | Fetch with ETag → parse → keep 2027 → group → save, in one transaction | all of the above |
| `internal/db` | Connect, run migrations (`migrations/*.sql`) | — |
| `cmd/*` | Tiny `main`s that wire the pieces together | — |

## Rules the database enforces

| Table | Unique on |
|---|---|
| companies | `normalized_name` |
| sources | `name` |
| listings | `dedupe_key` = company + title + job type + season |
| listing_sources | `(listing_id, source_id, source_listing_id)` |

Each has a test in `internal/store/store_test.go`.
