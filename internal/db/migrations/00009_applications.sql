-- Slice 9: the application tracker.

-- +goose Up

-- A named version of your resume, e.g. "Backend v2 · Nov 2026". A label
-- only (no file): it is all the resume stats need.
CREATE TABLE resume_versions (
    id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id    BIGINT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    label      TEXT NOT NULL CHECK (label <> ''),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (id, user_id)   -- lets applications check a resume is the same user's (below)
);
-- One label per user, ignoring case.
CREATE UNIQUE INDEX resume_versions_label_key ON resume_versions (user_id, lower(label));

-- One user's application to one listing.
CREATE TABLE applications (
    id                BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id           BIGINT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    -- RESTRICT: a listing with applications can't be deleted; merging
    -- listings moves applications first (see store.mergeInto).
    listing_id        BIGINT NOT NULL REFERENCES listings (id) ON DELETE RESTRICT,
    resume_version_id BIGINT,
    applied_on        DATE,                 -- editable; NULL while only saved
    stage             TEXT NOT NULL CHECK (stage IN
        ('saved', 'applied', 'online_assessment', 'interview', 'offer', 'rejected', 'withdrawn')),
    interview_round   INT CHECK (interview_round >= 1),
    expected_rounds   INT CHECK (expected_rounds >= 1),
    notes             TEXT NOT NULL DEFAULT '',
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- You apply to a job once: a double-click can't make two.
    UNIQUE (user_id, listing_id),
    -- The resume must be one of this user's own versions.
    FOREIGN KEY (resume_version_id, user_id) REFERENCES resume_versions (id, user_id),
    -- A round number exactly when interviewing.
    CHECK ((stage = 'interview') = (interview_round IS NOT NULL)),
    -- Past "saved", you must have applied on some date.
    CHECK (stage = 'saved' OR applied_on IS NOT NULL)
);
CREATE INDEX applications_listing_idx ON applications (listing_id);

-- Every stage change, so the history stays true even after fixing a
-- mistake (a fix is just another move). from_stage is NULL for the first.
CREATE TABLE application_events (
    id              BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    application_id  BIGINT NOT NULL REFERENCES applications (id) ON DELETE CASCADE,
    from_stage      TEXT,
    to_stage        TEXT NOT NULL,
    interview_round INT,
    at              TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX application_events_app_idx ON application_events (application_id, at);

-- +goose Down
DROP TABLE application_events;
DROP TABLE applications;
DROP TABLE resume_versions;
