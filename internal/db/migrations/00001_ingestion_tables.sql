-- Slice 1: the tables ingestion needs.
--
-- Every "only one of these may exist" rule lives here as a UNIQUE constraint,
-- so the database enforces it even if Go code has a bug.

-- +goose Up

-- A company, e.g. "Stripe". "Stripe" and "Stripe, Inc." normalize to the
-- same normalized_name, so they are one row.
CREATE TABLE companies (
    id              BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name            TEXT NOT NULL,          -- display name, as first seen
    normalized_name TEXT NOT NULL UNIQUE
);

-- Where listings come from (one row per GitHub repo we read).
CREATE TABLE sources (
    id              BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name            TEXT NOT NULL UNIQUE,   -- "simplify", "speedyapply"
    format          TEXT NOT NULL CHECK (format IN ('json', 'markdown')),
    url             TEXT NOT NULL,
    etag            TEXT,                   -- from the last 200 response; lets us skip unchanged files
    last_fetched_at TIMESTAMPTZ
);

INSERT INTO sources (name, format, url) VALUES
    ('simplify', 'json',
     'https://raw.githubusercontent.com/SimplifyJobs/Summer2027-Internships/dev/.github/scripts/listings.json'),
    ('speedyapply', 'markdown',
     'https://raw.githubusercontent.com/speedyapply/2027-SWE-College-Jobs/main/README.md');

-- One real job: a company + title + job type + season.
-- The same job posted for several cities, or in several sources, is ONE listing.
CREATE TABLE listings (
    id            BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    company_id    BIGINT NOT NULL REFERENCES companies (id),
    title         TEXT NOT NULL,            -- display title, as first seen
    job_type      TEXT NOT NULL CHECK (job_type IN ('internship', 'new_grad')),
    season        TEXT NOT NULL,            -- "Summer 2027"
    degree_level  TEXT NOT NULL CHECK (degree_level IN ('bachelors_ok', 'advanced_required')),
    dedupe_key    TEXT NOT NULL UNIQUE,     -- see listing.DedupeKey in Go
    status        TEXT NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'closed')),
    first_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    closed_at     TIMESTAMPTZ,
    -- A closed listing must say when it closed; an open one must not.
    CHECK ((status = 'closed') = (closed_at IS NOT NULL))
);

-- The feed sorts freshest-first, so index that.
CREATE INDEX listings_first_seen_idx ON listings (first_seen_at DESC);

-- One posting of a listing in one source: its own apply link and cities.
-- A listing can have many (SIG posts the same role for 4 offices), and one
-- source record can feed several listings (a posting for Summer AND Fall 2027).
CREATE TABLE listing_sources (
    id                BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    listing_id        BIGINT NOT NULL REFERENCES listings (id) ON DELETE CASCADE,
    source_id         BIGINT NOT NULL REFERENCES sources (id),
    source_listing_id TEXT NOT NULL,        -- the source's own id for the posting
    apply_url         TEXT NOT NULL,
    locations         TEXT[] NOT NULL DEFAULT '{}',
    active            BOOLEAN NOT NULL,     -- what the source said last time we looked
    first_seen_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (listing_id, source_id, source_listing_id)
);

-- +goose Down
DROP TABLE listing_sources;
DROP TABLE listings;
DROP TABLE sources;
DROP TABLE companies;
