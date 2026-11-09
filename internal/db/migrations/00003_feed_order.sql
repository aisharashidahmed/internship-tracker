-- Slice 4: the feed's "freshest first" order.

-- +goose Up

-- When the source says the job was posted. Simplify gives a timestamp;
-- SpeedyApply gives an age ("23d"), so its date is only accurate to a day.
ALTER TABLE listing_sources ADD COLUMN posted_at TIMESTAMPTZ;

ALTER TABLE listings
    -- The earliest posted_at of any of its postings; NULL if no source says.
    ADD COLUMN posted_at TIMESTAMPTZ,
    -- True if the listing was created during its source's very first ingest.
    ADD COLUMN from_first_load BOOLEAN NOT NULL DEFAULT false;

-- What the feed sorts by. Listings from a first ingest all share one
-- first_seen_at, which would make the order meaningless, so they use the
-- source's posted date. Listings that appear later use first_seen_at: the
-- moment the app could first show them to you. Postgres computes this
-- column itself, so it can never disagree with the columns it comes from.
ALTER TABLE listings ADD COLUMN feed_at TIMESTAMPTZ
    GENERATED ALWAYS AS (CASE WHEN from_first_load THEN posted_at ELSE first_seen_at END) STORED;

-- The feed query: open before stale, then freshest first; unknown dates last.
DROP INDEX listings_first_seen_idx;
CREATE INDEX listings_feed_idx ON listings
    ((status = 'stale'), feed_at DESC NULLS LAST, posted_at DESC NULLS LAST, id DESC)
    WHERE status IN ('open', 'stale');

-- +goose Down
DROP INDEX listings_feed_idx;
CREATE INDEX listings_first_seen_idx ON listings (first_seen_at DESC);
ALTER TABLE listings DROP COLUMN feed_at, DROP COLUMN from_first_load, DROP COLUMN posted_at;
ALTER TABLE listing_sources DROP COLUMN posted_at;
