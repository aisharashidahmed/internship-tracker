# internship-tracker (name TBD)

Pulls internship listings from public GitHub repos, merges duplicates, and
(soon) tracks your applications. Spec: `Internship Tracker — Project Spec.pdf`.

## Run it

Needs Go, and Postgres running locally (`brew services start postgresql@18`).

```sh
createdb internship_tracker          # once
createdb internship_tracker_test     # once, for tests
go run ./cmd/migrate                 # create/update tables
go run ./cmd/ingest                  # fetch every source and save listings
go run ./cmd/ingest -force           # ignore saved ETags and re-save everything
go test ./...                        # all tests (store tests use the _test DB)
```

Set `DATABASE_URL` / `TEST_DATABASE_URL` to use other databases.

## Sources

| Source | File | Gives us |
|---|---|---|
| [Simplify](https://github.com/SimplifyJobs/Summer2027-Internships) | `.github/scripts/listings.json` (branch `dev`) | ids, seasons, degrees, an explicit closed flag |
| [SpeedyApply](https://github.com/speedyapply/2027-SWE-College-Jobs) | `README.md` (Markdown tables) | US SWE internships; no ids, seasons only when the title says so |

If one source fails (download error or format change), the others still run,
its data is left unchanged, and `ingest` exits with status 1.

## How two postings become one listing

1. **Same job link** — the apply links point at the same job ad
   (e.g. both are Greenhouse job `6241542004`, however each source spelled the URL).
   Generic links like `company.com/careers` are never used.
2. **Same dedupe key** — company + title + job type + season, after cleanup
   ("SWE" = "Software Engineer", "Stripe, Inc." = "Stripe", "– Summer 2027" dropped).
3. When unsure, keep them apart. Every run prints a **possible duplicates**
   report of near-misses to review.

## Listing status

| Status | Meaning |
|---|---|
| `open` | some source lists it as active |
| `stale` | no source says closed, but it vanished from every source that listed it — "may be closed" |
| `closed` | a source explicitly marked it closed |

## How the code is laid out

Data flows one way: **source file → postings → listings → database**.

| Path | Job | Knows about |
|---|---|---|
| `internal/listing` | Common types, name cleanup, `DedupeKey`, `JobLink`, `Group`, duplicates report | nothing else |
| `internal/source` | The `Adapter` interface | `listing` |
| `internal/source/simplify` | Parse Simplify's `listings.json` | `listing` |
| `internal/source/speedyapply` | Parse SpeedyApply's Markdown tables | `listing` |
| `internal/store` | All SQL: find/create listings (by job link, then key), save postings, status | `listing` |
| `internal/ingest` | Fetch with ETag → parse → keep 2027 → group → save → mark missing, in one transaction | all of the above |
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
