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
go run ./cmd/invite -note "Jordan"   # optional: a sign-up link for a person or group (-uses 50)
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

Colors come from five: Wild Berry `#621E3E` (buttons, headings; dark-mode
backgrounds), Chrysanthemum `#D9828D` (selected filters, links), Cypress
`#667436` ("Posted today"), Bamboo `#C6C954` (secondary chips) and Ylang Ylang
`#FFEC8E` ("New for you"; the light-mode background tint). Every text/background
pair meets WCAG AA (4.5:1); where a raw color didn't, a deeper shade is used
(e.g. light-mode links are `#A13D52`). Text is 17px on laptops and up.

On desktop, filters sit in a 260px sidebar and cards fill columns of at least
380px: 2 on a laptop, 3 on a big monitor (the page is about 1400px wide, 1680px
from 1920px screens). On phones, one column, and the filters slide up from a
**Filters** button (all tap targets 44px or more).

- **Search** company or title; a **live count** ("312 roles match") updates as
  you type or change filters.
- **One card per role**: the same role at the same company in several seasons
  shows as one card with season chips (Summer / Spring / ...). Pick a season;
  Apply, Save and I applied act on that season. Listings stay separate in the
  database, and cards are grouped before paging so none is split across pages.
- Each company gets a colored initial circle (from a hash of its name).
- **Posted today** (the source's posted date is today, in your timezone) and
  **New for you** (since your last visit).

Two tabs, **Internships** (default) and **New grad**: a title containing
"New Grad" makes a role new-grad even in an internship list. One card per
listing, open roles first, freshest first; stale ones ("may be
closed") go at the end and closed ones are hidden. A card with several job
links shows "N openings" and expands to list each link (a link both sources
list counts once).

"Freshest" means: for listings from a source's very first ingest, the date
the source says the job was posted (they'd otherwise all share one
first-seen time); for listings found later, when this app first saw them,
unless the job was already more than 14 days old (by its posted date) when
found: then its posted date, so an old discovery doesn't jump to the top. It
still gets "New for you", since it's new to you. Unknown dates go last.

## Tags and filters

**Role tags** (a listing can have several): General SWE, Backend, Frontend,
ML, AI, Data, PM, Quant, Hardware. ML needs an ML-specific term (machine
learning, deep/reinforcement learning, computer vision, NLP, LLM, ...); a title
with just "AI" gets the separate AI tag. They come from the source's own section
(Simplify's Quant / Hardware / Product / AI/ML/Data, SpeedyApply's Quant) and
from whole-word title keywords ("Backend", "Machine Learning", "APM", ...).
General SWE means no more specific signal. Simplify's Product section gives PM
only when the title says "product" (it also holds marketing and payments
roles); those others get no role tag rather than a wrong one.

**Industry tags** are per company, from `internal/listing/tags.go`: an
*always* list, a *never* list, an *approved* list, and whole-word keywords
("Health", "Medical", ...). `cmd/ingest` prints an **industry review** of
keyword matches nobody has approved yet; move each to *approved* or *never*.

**Top companies** come only from the sources' own signals: Simplify's README
marks FAANG+ companies with 🔥 (its JSON has no such field), and SpeedyApply
has a FAANG+ section. Each signal is stored per company and synced on its own
(the README has its own ETag). The **Tier → ★ Top companies** filter is first
in the sidebar; matching cards show a "★ Top company" chip.

**Filters** live in the URL (`?type=internship&role=backend&role=ml&match=all&industry=healthcare`).
Roles match **any** ("Backend / ML") or **all** ("Backend + ML"); different
filter groups always combine with AND.

## New since you last looked

Logged in, the feed shows "N new since Tue 9:00 AM", a **New** chip on each
new card, and an **Only new** filter. "New" means it appeared after you last
looked. That moves forward on its own only when you come back after 30+
minutes away (refreshing or paging keeps your badges), or when you click
**Mark all as seen**. On your first visit, the last 24 hours count as new.

## Application tracker

Logged in, each feed card has **Save** and **I applied**. *I applied* opens
the application, where you pick a resume version (a label like "Backend v2 ·
Nov 2026"), fix the applied date (defaults to today), set expected interview
rounds, and keep notes (e.g. which apply link you used).

- Stages: saved, applied, online assessment, interview (numbered rounds),
  offer, rejected, withdrawn. Any move is allowed and every move is kept in
  the history, so fixing a mistake is just another move.
- **My applications** lists everything by stage, furthest along first.
- The feed hides listings you've applied to (it's for "what next?");
  **Show applied** brings them back with your stage on the card.
- The database enforces one application per user and listing (a
  double-click can't make two), resume labels unique per user, and that an
  application's resume belongs to the same user.

## When the matching rules change

The rules live in `internal/listing`, and the app fingerprints that code.
When it changes, the next ingest re-saves every source once (even if GitHub
says the file is unchanged). A posting that now matches a different listing
leaves its old one; an old listing left with no postings is merged into the
new one, unless the same person applied to both, in which case both are kept and
listed as possible duplicates. A merge moves applications to the remaining listing.

## Closed-listing detection

Sources are sometimes slow to mark a job closed, so the server also checks
each job-specific apply link once a day (generic careers pages are never
checked). Only clear signals count: HTTP 404/410, or Greenhouse redirecting a
job link back to the company's board. Anything ambiguous (timeouts, 403/429
bot blocking, 5xx outages, Workday's JavaScript pages) is "couldn't tell" and
never closes anything. After **3 clear failures in a row** the link is dead;
a listing closes when none of its postings is still active with a working
link. Closed listings say why: "Closed · source marked closed" or
"Closed · link no longer works". A dead link that comes back reopens it.

Politeness: robots.txt is read and obeyed (cached a day), at most one
request every 5 seconds to any website, a user agent naming this project,
at most 3 same-site redirects, and only the first 64 KB of a page is read.
`go run ./cmd/server -check-links-every=0` turns checking off.

## Time zones

Times are stored as absolute moments (`timestamptz`), and every database
connection works in UTC, whatever the Postgres server is set to. Showing a
time, and deciding what "today" is (e.g. the default applied date), uses
**your** timezone: your browser reports it at sign-up and login. Logged-out
visitors get `APP_TIMEZONE` (default `America/New_York`).

## Accounts

The feed is public; an account adds personal features. Anyone can **sign up**
at `/signup` (at most 5 new accounts per network per hour). Invite links still
work for sharing with a group: `go run ./cmd/invite` prints one (single-use by
default, `-uses N` for a group, `-days N` to expire). Logins last 30 days and
renew while you use the site.

- Passwords: at least 10 characters (no symbol rules), stored as bcrypt hashes.
- Sessions: a random token in an `HttpOnly`, `SameSite=Lax` cookie (`Secure`
  with `COOKIE_SECURE=1`); the database stores only its SHA-256 hash.
- Failed logins are limited per email (5) and per IP (20) per 15 minutes.
- Form posts from other websites are rejected (Go's `CrossOriginProtection`).
- Messages never reveal whether an email has an account.

## Deploying (use it from any device)

The repo includes a `Dockerfile` and a Render Blueprint (`render.yaml`) that
creates the web service and a Postgres database together:

1. Sign in at render.com (with GitHub is easiest) and give it access to this repo.
2. **New → Blueprint**, pick this repo, review the plans and prices, and confirm.
3. Wait for the first deploy. On startup the server migrates the database,
   ingests both sources, and starts the hourly jobs. Open the
   `https://….onrender.com` address on any device and sign up.

Every push to `main` deploys again. The same image runs on any Docker host:
set `DATABASE_URL` and `PORT`, plus `COOKIE_SECURE=1` (HTTPS) and
`TRUST_PROXY=1` (behind the host's proxy). Run exactly one instance: the
hourly jobs and the login rate limits live inside it.

## How the code is laid out

Data flows one way: **source file → postings → listings → database**.

| Path | Job | Knows about |
|---|---|---|
| `internal/listing` | Common types, name cleanup, `DedupeKey`, `JobLink`, `Group`, duplicates report | nothing else |
| `internal/source` | The `Adapter` interface | `listing` |
| `internal/source/simplify` | Parse Simplify's `listings.json` | `listing` |
| `internal/source/speedyapply` | Parse SpeedyApply's Markdown tables | `listing` |
| `internal/auth` | Password hashing, tokens, email/password rules, login rate limiter | — |
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
