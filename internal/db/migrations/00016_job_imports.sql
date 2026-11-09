-- "Add jobs I applied to" by pasting links (decided 2026-11-09).

-- +goose Up

-- Listings people add themselves come from this pseudo-source. Ingestion
-- never reads it (there's no adapter), so it never marks them missing.
ALTER TABLE sources DROP CONSTRAINT sources_format_check;
ALTER TABLE sources ADD CONSTRAINT sources_format_check CHECK (format IN ('json', 'markdown', 'manual'));
INSERT INTO sources (name, format, url) VALUES ('user-added', 'manual', '');

-- A private listing belongs to one user and is visible only to them. NULL
-- means public. Private listings never appear in the feed, counts, link
-- checks, notifications or reports.
ALTER TABLE listings ADD COLUMN owner_user_id BIGINT REFERENCES users (id) ON DELETE CASCADE;
CREATE INDEX listings_owner_idx ON listings (owner_user_id) WHERE owner_user_id IS NOT NULL;

-- Gate 5 of sharing needs a verified email. Verification isn't built yet,
-- so this stays NULL (and sharing says "Verify your email to share").
ALTER TABLE users ADD COLUMN email_verified_at TIMESTAMPTZ;

-- The background queue: one row per pasted link. Workers claim rows with
-- FOR UPDATE SKIP LOCKED, so two workers never take the same row.
CREATE TABLE job_imports (
    id              BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id         BIGINT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    url             TEXT NOT NULL,
    status          TEXT NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'running', 'ready', 'saved', 'failed')),
    attempts        INT NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_error      TEXT NOT NULL DEFAULT '',
    -- Which step of the chain produced the result: the stats this feature
    -- reports on ("match" > "api" > "jsonld" > "form").
    step            TEXT CHECK (step IN ('match', 'api', 'jsonld', 'form')),
    company         TEXT NOT NULL DEFAULT '',
    title           TEXT NOT NULL DEFAULT '',
    location        TEXT NOT NULL DEFAULT '',
    season          TEXT NOT NULL DEFAULT '',
    job_type        TEXT NOT NULL DEFAULT 'internship' CHECK (job_type IN ('internship', 'new_grad')),
    listing_id      BIGINT REFERENCES listings (id) ON DELETE SET NULL, -- an existing listing it matched
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at     TIMESTAMPTZ
);
CREATE INDEX job_imports_queue_idx ON job_imports (next_attempt_at) WHERE status = 'queued';
CREATE INDEX job_imports_user_idx ON job_imports (user_id, created_at DESC);

-- Extraction results by job identity (the canonical job-ID link), so the
-- same job is never fetched and parsed twice, whoever pastes it.
CREATE TABLE extraction_cache (
    job_key    TEXT PRIMARY KEY,
    step       TEXT NOT NULL CHECK (step IN ('api', 'jsonld')),
    company    TEXT NOT NULL,
    title      TEXT NOT NULL,
    location   TEXT NOT NULL DEFAULT '',
    season     TEXT NOT NULL DEFAULT '',
    job_type   TEXT NOT NULL CHECK (job_type IN ('internship', 'new_grad')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Requests to make a private listing public, and how review went.
CREATE TABLE share_requests (
    id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id    BIGINT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    listing_id BIGINT REFERENCES listings (id) ON DELETE SET NULL,
    outcome    TEXT NOT NULL CHECK (outcome IN ('public', 'merged', 'private', 'review')),
    reason     TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX share_requests_user_idx ON share_requests (user_id, created_at);

-- +goose Down
DROP TABLE share_requests;
DROP TABLE extraction_cache;
DROP TABLE job_imports;
ALTER TABLE users DROP COLUMN email_verified_at;
DROP INDEX listings_owner_idx;
DELETE FROM listings WHERE owner_user_id IS NOT NULL;
ALTER TABLE listings DROP COLUMN owner_user_id;
DELETE FROM sources WHERE name = 'user-added';
ALTER TABLE sources DROP CONSTRAINT sources_format_check;
ALTER TABLE sources ADD CONSTRAINT sources_format_check CHECK (format IN ('json', 'markdown'));
