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
go run ./cmd/server                  # feed at http://localhost:8080 + ingest now, then hourly
go run ./cmd/server -ingest-every=0  # feed only (no ingestion)
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

## Hourly ingestion

The server ingests every source as soon as it starts, then every hour (no
catch-up after downtime: each file is a snapshot of right now). An unchanged
source costs one conditional request: GitHub answers HTTP 304 and nothing is
parsed or written. Every run logs one line per source, so a quiet hour still
shows the scheduler is alive:

```
2026/11/08 20:17:48 simplify: unchanged (HTTP 304) in 30ms
2026/11/08 20:17:28 speedyapply: 827 postings, 396 new, 336 updated, 1 unchanged, 0 stale, 0 closed in 368ms
```

A Postgres advisory lock lets only one ingest write a given source at a time,
so a manual `go run ./cmd/ingest` while the server runs is safe.

## The feed

Two tabs, **Internships** (default) and **New grad**: a title containing
"New Grad" makes a role new-grad even in an internship list. One card per
listing, open roles first, freshest first; stale ones ("may be
closed") go at the end and closed ones are hidden. A card with several job
links shows "N openings" and expands to list each link (a link both sources
list counts once).

"Freshest" means: for listings from a source's very first ingest, the date
the source says the job was posted (they'd otherwise all share one
first-seen time); for listings found later, when this app first saw them.
Unknown dates go last.

## Tags and filters

**Role tags** (a listing can have several): General SWE, Backend, Frontend,
ML, Data, PM, Quant, Hardware. They come from the source's own section
(Simplify's Quant / Hardware / Product / AI/ML/Data, SpeedyApply's Quant) and
from whole-word title keywords ("Backend", "Machine Learning", "APM", ...).
General SWE means no more specific signal. Simplify's Product section gives PM
only when the title says "product" (it also holds marketing and payments
roles); those others get no role tag rather than a wrong one.

**Industry tags** are per company, from `internal/listing/tags.go`: an
*always* list, a *never* list, an *approved* list, and whole-word keywords
("Health", "Medical", ...). `cmd/ingest` prints an **industry review** of
keyword matches nobody has approved yet; move each to *approved* or *never*.

**Filters** live in the URL (`?type=internship&role=backend&role=ml&match=all&industry=healthcare`).
Roles match **any** ("Backend / ML") or **all** ("Backend + ML"); different
filter groups always combine with AND.

## How the code is laid out

Data flows one way: **source file → postings → listings → database**.

| Path | Job | Knows about |
|---|---|---|
| `internal/listing` | Common types, name cleanup, `DedupeKey`, `JobLink`, `Group`, duplicates report | nothing else |
| `internal/source` | The `Adapter` interface | `listing` |
| `internal/source/simplify` | Parse Simplify's `listings.json` | `listing` |
| `internal/source/speedyapply` | Parse SpeedyApply's Markdown tables | `listing` |
| `internal/feed` | Turns a stored listing into a feed card (city summary, openings) | `store` |
| `internal/web` | HTML page and JSON API for the feed, one shared loader | `feed`, `store` |
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
