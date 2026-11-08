-- Slice 3: a second source (SpeedyApply), matching by apply link, and "stale".

-- +goose Up

-- "stale": no source says the job is closed, but the only postings that said
-- it was open have disappeared from their source. Shown with "may be closed".
ALTER TABLE listings DROP CONSTRAINT listings_status_check;
ALTER TABLE listings ADD CONSTRAINT listings_status_check
    CHECK (status IN ('open', 'stale', 'closed'));

-- SpeedyApply doesn't say which degrees a role accepts. Store "unknown"
-- instead of guessing.
ALTER TABLE listings DROP CONSTRAINT listings_degree_level_check;
ALTER TABLE listings ADD CONSTRAINT listings_degree_level_check
    CHECK (degree_level IN ('bachelors_ok', 'advanced_required', 'unknown'));

ALTER TABLE listing_sources
    -- Set when the posting was missing from its source's latest file
    -- ("not seen since"); cleared when it shows up again.
    ADD COLUMN missing_since TIMESTAMPTZ,
    -- The job's identity taken from its apply link, e.g. "greenhouse:7012345".
    -- NULL for generic links (company.com/careers), which never match.
    ADD COLUMN job_link TEXT;

-- Matching looks postings up by job link.
CREATE INDEX listing_sources_job_link_idx ON listing_sources (job_link)
    WHERE job_link IS NOT NULL;

-- +goose Down
DROP INDEX listing_sources_job_link_idx;
ALTER TABLE listing_sources DROP COLUMN job_link, DROP COLUMN missing_since;
UPDATE listings SET degree_level = 'bachelors_ok' WHERE degree_level = 'unknown';
ALTER TABLE listings DROP CONSTRAINT listings_degree_level_check;
ALTER TABLE listings ADD CONSTRAINT listings_degree_level_check
    CHECK (degree_level IN ('bachelors_ok', 'advanced_required'));
UPDATE listings SET status = 'open' WHERE status = 'stale';
ALTER TABLE listings DROP CONSTRAINT listings_status_check;
ALTER TABLE listings ADD CONSTRAINT listings_status_check
    CHECK (status IN ('open', 'closed'));
