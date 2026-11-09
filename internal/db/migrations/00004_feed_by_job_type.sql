-- Slice 5: the feed shows one job type at a time (Internships / New grad tabs).

-- +goose Up
-- Same order as before, with job_type first so each tab reads its own
-- part of the index.
DROP INDEX listings_feed_idx;
CREATE INDEX listings_feed_idx ON listings
    (job_type, (status = 'stale'), feed_at DESC NULLS LAST, posted_at DESC NULLS LAST, id DESC)
    WHERE status IN ('open', 'stale');

-- +goose Down
DROP INDEX listings_feed_idx;
CREATE INDEX listings_feed_idx ON listings
    ((status = 'stale'), feed_at DESC NULLS LAST, posted_at DESC NULLS LAST, id DESC)
    WHERE status IN ('open', 'stale');
