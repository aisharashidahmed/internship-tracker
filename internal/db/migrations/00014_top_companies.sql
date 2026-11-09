-- Top companies, from the sources' own signals (no hand-made list):
-- Simplify's README marks FAANG+ companies with 🔥; SpeedyApply lists them
-- in its "FAANG+" section.

-- +goose Up

-- One row per company per signal. A company is "top" if it has any row.
-- Each signal is synced on its own, so if Simplify drops its 🔥 for a
-- company, SpeedyApply's FAANG+ row (if any) still counts.
CREATE TABLE company_top_signals (
    company_id BIGINT NOT NULL REFERENCES companies (id) ON DELETE CASCADE,
    signal     TEXT NOT NULL CHECK (signal IN ('simplify-readme', 'speedyapply-faang')),
    PRIMARY KEY (company_id, signal)
);

-- Extra files read only for signals (not listings), with their ETags so an
-- unchanged file costs one cheap request.
CREATE TABLE signal_files (
    name            TEXT PRIMARY KEY,
    url             TEXT NOT NULL,
    etag            TEXT,
    last_fetched_at TIMESTAMPTZ
);
INSERT INTO signal_files (name, url) VALUES
    ('simplify-readme', 'https://raw.githubusercontent.com/SimplifyJobs/Summer2027-Internships/dev/README.md');

-- +goose Down
DROP TABLE signal_files;
DROP TABLE company_top_signals;
