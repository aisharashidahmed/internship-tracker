-- Slice 6: role tags (per listing) and industry tags (per company).

-- +goose Up

-- The roles one source's posting implies (from its title and the source's
-- own section). Kept per posting so each source's view survives the other
-- source's runs; listing_roles below is their combination.
ALTER TABLE listing_sources ADD COLUMN roles TEXT[] NOT NULL DEFAULT '{}';

-- A listing's role tags: the union of its postings' roles, minus
-- "general_swe" when anything more specific applies. One row per tag.
CREATE TABLE listing_roles (
    listing_id BIGINT NOT NULL REFERENCES listings (id) ON DELETE CASCADE,
    role       TEXT NOT NULL CHECK (role IN
        ('general_swe', 'backend', 'frontend', 'ml', 'data', 'pm', 'quant', 'hardware')),
    PRIMARY KEY (listing_id, role)   -- a listing has each tag at most once
);
-- The role filter looks listings up by tag.
CREATE INDEX listing_roles_role_idx ON listing_roles (role, listing_id);

-- Industries we know how to tag. Adding an industry = a row here plus a
-- rule in internal/listing/tags.go.
CREATE TABLE industries (
    name TEXT PRIMARY KEY
);
INSERT INTO industries (name) VALUES ('healthcare');

-- A company's industry tags.
CREATE TABLE company_industries (
    company_id BIGINT NOT NULL REFERENCES companies (id) ON DELETE CASCADE,
    industry   TEXT NOT NULL REFERENCES industries (name),
    by_keyword BOOLEAN NOT NULL,     -- tagged only because of a keyword: worth a human look
    PRIMARY KEY (company_id, industry)
);
CREATE INDEX company_industries_industry_idx ON company_industries (industry, company_id);

-- +goose Down
DROP TABLE company_industries;
DROP TABLE industries;
DROP TABLE listing_roles;
ALTER TABLE listing_sources DROP COLUMN roles;
