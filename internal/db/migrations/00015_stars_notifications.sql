-- Starred companies and in-app notifications (decided 2026-11-08).

-- +goose Up

-- A user's starred companies. Starring twice is one star.
CREATE TABLE starred_companies (
    user_id    BIGINT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    company_id BIGINT NOT NULL REFERENCES companies (id) ON DELETE CASCADE,
    starred_at TIMESTAMPTZ NOT NULL DEFAULT now(),  -- only roles first seen after this notify
    PRIMARY KEY (user_id, company_id)
);

-- Which job type a user wants to hear about (not role tags: a star means
-- "I care about this company broadly").
ALTER TABLE users ADD COLUMN notify_job_type TEXT NOT NULL DEFAULT 'internship'
    CHECK (notify_job_type IN ('internship', 'new_grad', 'both'));

-- Notifications are events: "a new role appeared at a company you starred".
-- One per user, listing and kind, enforced here: a role seen in both sources,
-- or an ingest that runs twice, still pings exactly once.
CREATE TABLE notifications (
    id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id    BIGINT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    listing_id BIGINT NOT NULL REFERENCES listings (id) ON DELETE CASCADE,
    kind       TEXT NOT NULL CHECK (kind IN ('starred_company')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    read_at    TIMESTAMPTZ,
    UNIQUE (user_id, listing_id, kind)
);
CREATE INDEX notifications_user_idx ON notifications (user_id, created_at DESC);

-- +goose Down
DROP TABLE notifications;
ALTER TABLE users DROP COLUMN notify_job_type;
DROP TABLE starred_companies;
