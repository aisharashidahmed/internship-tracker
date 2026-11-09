-- Slice 8: "new since you last looked", and cleaning up after rule changes.

-- +goose Up

-- "Last looked" moves forward only after 30+ minutes away (or "Mark all as
-- seen"), so refreshing mid-visit keeps the New badges. last_feed_view_at is
-- the latest feed page view; last_seen_feed_at (from slice 7) is the
-- "new since" boundary.
ALTER TABLE users ADD COLUMN last_feed_view_at TIMESTAMPTZ;

-- Which version of the matching rules each source was last saved with.
-- When the rules change, the next run re-saves that source even if the
-- file itself is unchanged (HTTP 304).
ALTER TABLE sources ADD COLUMN rules_version TEXT;

-- Listings that a rule change would have merged, but that we kept apart
-- (e.g. the same user applied to each separately). Shown in the
-- possible-duplicates report. Stored once per pair, smaller id first.
CREATE TABLE listings_kept_apart (
    listing_a  BIGINT NOT NULL REFERENCES listings (id) ON DELETE CASCADE,
    listing_b  BIGINT NOT NULL REFERENCES listings (id) ON DELETE CASCADE,
    reason     TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (listing_a, listing_b),
    CHECK (listing_a < listing_b)
);

-- +goose Down
DROP TABLE listings_kept_apart;
ALTER TABLE sources DROP COLUMN rules_version;
ALTER TABLE users DROP COLUMN last_feed_view_at;
