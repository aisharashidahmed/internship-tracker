-- Slice 6: an "approved" list means a keyword match can be reviewed once;
-- the flag now says whether a person still needs to look at it.

-- +goose Up
ALTER TABLE company_industries RENAME COLUMN by_keyword TO needs_review;

-- +goose Down
ALTER TABLE company_industries RENAME COLUMN needs_review TO by_keyword;
