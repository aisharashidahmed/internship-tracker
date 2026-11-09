-- Slice 10: closed-listing detection by checking apply links.

-- +goose Up

ALTER TABLE listing_sources
    ADD COLUMN link_checked_at  TIMESTAMPTZ,              -- last check of any result
    ADD COLUMN link_result      TEXT CHECK (link_result IN ('alive', 'dead', 'unknown')),
    ADD COLUMN link_detail      TEXT NOT NULL DEFAULT '', -- e.g. "HTTP 404", "robots.txt disallows"
    -- Clear "dead" results in a row; "couldn't tell" results don't count
    -- either way. At 3 the link is dead.
    ADD COLUMN link_failures    INT NOT NULL DEFAULT 0 CHECK (link_failures >= 0),
    ADD COLUMN link_dead_at     TIMESTAMPTZ;

-- Which links are due: job-specific links not checked in the last day.
CREATE INDEX listing_sources_link_due_idx ON listing_sources (link_checked_at NULLS FIRST)
    WHERE job_link IS NOT NULL;

-- Why a closed listing is closed, shown on the Applications page:
-- 'source' = a source marked it closed; 'link' = its apply links stopped working.
ALTER TABLE listings ADD COLUMN closed_reason TEXT CHECK (closed_reason IN ('source', 'link'));
UPDATE listings SET closed_reason = 'source' WHERE status = 'closed';
ALTER TABLE listings ADD CONSTRAINT listings_closed_reason_check2
    CHECK ((status = 'closed') = (closed_reason IS NOT NULL));

-- +goose Down
ALTER TABLE listings DROP CONSTRAINT listings_closed_reason_check2;
ALTER TABLE listings DROP COLUMN closed_reason;
DROP INDEX listing_sources_link_due_idx;
ALTER TABLE listing_sources
    DROP COLUMN link_dead_at, DROP COLUMN link_failures, DROP COLUMN link_detail,
    DROP COLUMN link_result, DROP COLUMN link_checked_at;
