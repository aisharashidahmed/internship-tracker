-- Each user's own timezone (an IANA name like "America/New_York"), sent by
-- their browser at sign-up and login. Every time shown to them, and what
-- "today" means for them, uses it. Stored times stay absolute (timestamptz).

-- +goose Up
ALTER TABLE users ADD COLUMN timezone TEXT NOT NULL DEFAULT 'America/New_York';

-- +goose Down
ALTER TABLE users DROP COLUMN timezone;
