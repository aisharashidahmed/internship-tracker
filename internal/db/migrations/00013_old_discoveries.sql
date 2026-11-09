-- Feed order: an old discovery doesn't jump to the top (decided 2026-11-08).

-- +goose Up

-- "New for you" keeps the old meaning of feed_at: when the app could first
-- show the listing (its posted date for a source's first ingest, otherwise
-- first_seen_at). A listing the app only now discovered is new to you,
-- even if it was posted long ago.
ALTER TABLE listings ADD COLUMN new_at TIMESTAMPTZ
    GENERATED ALWAYS AS (CASE WHEN from_first_load THEN posted_at ELSE first_seen_at END) STORED;

-- The sort time: as before, except that a listing already more than 14 days
-- old (by its posted date) when the app first saw it sorts by its posted
-- date, so a 69-day-old role found today lands where it belongs, not on top.
-- No posted date: first_seen_at, as before. (A generated column's formula
-- can't be edited in place on every Postgres version, so it is rebuilt.)
DROP INDEX listings_feed_idx;
ALTER TABLE listings DROP COLUMN feed_at;
ALTER TABLE listings ADD COLUMN feed_at TIMESTAMPTZ
    GENERATED ALWAYS AS (CASE
        WHEN from_first_load THEN posted_at
        WHEN posted_at IS NOT NULL AND first_seen_at - posted_at > interval '14 days' THEN posted_at
        ELSE first_seen_at
    END) STORED;
CREATE INDEX listings_feed_idx ON listings
    (job_type, (status = 'stale'), feed_at DESC NULLS LAST, posted_at DESC NULLS LAST, id DESC)
    WHERE status IN ('open', 'stale');

-- +goose Down
DROP INDEX listings_feed_idx;
ALTER TABLE listings DROP COLUMN feed_at;
ALTER TABLE listings ADD COLUMN feed_at TIMESTAMPTZ
    GENERATED ALWAYS AS (CASE WHEN from_first_load THEN posted_at ELSE first_seen_at END) STORED;
CREATE INDEX listings_feed_idx ON listings
    (job_type, (status = 'stale'), feed_at DESC NULLS LAST, posted_at DESC NULLS LAST, id DESC)
    WHERE status IN ('open', 'stale');
ALTER TABLE listings DROP COLUMN new_at;
