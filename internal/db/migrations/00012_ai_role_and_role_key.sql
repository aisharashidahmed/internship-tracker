-- Slice 11: the AI tag, and grouping one role's seasons into one card.

-- +goose Up
ALTER TABLE listing_roles DROP CONSTRAINT listing_roles_role_check;
ALTER TABLE listing_roles ADD CONSTRAINT listing_roles_role_check CHECK (role IN
    ('general_swe', 'backend', 'frontend', 'ml', 'ai', 'data', 'pm', 'quant', 'hardware'));

-- The same role at the same company, ignoring season (the dedupe key
-- without the season). The feed shows one card per role_key, with a chip
-- per season; the listings themselves stay separate. Filled in by Go on
-- every save (the rules-version change re-saves everything once).
ALTER TABLE listings ADD COLUMN role_key TEXT;
CREATE INDEX listings_role_key_idx ON listings (role_key);

-- +goose Down
DROP INDEX listings_role_key_idx;
ALTER TABLE listings DROP COLUMN role_key;
DELETE FROM listing_roles WHERE role = 'ai';
ALTER TABLE listing_roles DROP CONSTRAINT listing_roles_role_check;
ALTER TABLE listing_roles ADD CONSTRAINT listing_roles_role_check CHECK (role IN
    ('general_swe', 'backend', 'frontend', 'ml', 'data', 'pm', 'quant', 'hardware'));
