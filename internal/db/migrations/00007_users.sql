-- Slice 7: accounts, sessions and invites.

-- +goose Up

-- An invite link: sign-up is invite-only. max_uses = 1 for a personal link;
-- larger for a group link (e.g. one shared with a club).
CREATE TABLE invites (
    id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    token_hash BYTEA NOT NULL UNIQUE,   -- SHA-256 of the token in the link; the token itself is never stored
    note       TEXT NOT NULL DEFAULT '', -- who it was for, e.g. "ColorStack Discord"
    max_uses   INT NOT NULL CHECK (max_uses > 0),
    uses       INT NOT NULL DEFAULT 0,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- The database itself refuses an over-used invite, even if Go has a bug.
    CHECK (uses BETWEEN 0 AND max_uses)
);

CREATE TABLE users (
    id                BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    email             TEXT NOT NULL,         -- as typed (trimmed); compared lowercased
    password_hash     TEXT NOT NULL,         -- bcrypt; includes its own salt and cost
    invite_id         BIGINT REFERENCES invites (id),
    last_seen_feed_at TIMESTAMPTZ,           -- powers "new since you last looked" (next slice)
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- One account per email, ignoring case: "Aisha@x.com" and "aisha@x.com" are the same person.
CREATE UNIQUE INDEX users_email_key ON users (lower(email));

-- A logged-in browser. The cookie holds a random token; we store only its
-- hash, so a leaked database can't be used to log in as anyone.
CREATE TABLE sessions (
    id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id      BIGINT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    token_hash   BYTEA NOT NULL UNIQUE,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_used_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at   TIMESTAMPTZ NOT NULL      -- 30 days after last use
);
CREATE INDEX sessions_user_idx ON sessions (user_id);

-- +goose Down
DROP TABLE sessions;
DROP TABLE users;
DROP TABLE invites;
