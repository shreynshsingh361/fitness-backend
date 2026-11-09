-- A much bigger food catalogue, imported by scripts/import-foods.sh from two open
-- databases (the app shows credit for both):
--   usda: USDA FoodData Central, generic foods (rice, dal, egg). Public domain.
--   off:  Open Food Facts, packaged products. Open Database License (ODbL).
-- The foods seeded in 002 stay as source 'hxvy'.

-- Fast "contains" search over millions of names (trigram index).
CREATE EXTENSION IF NOT EXISTS pg_trgm;

-- Different products can share a name ("Peanut butter" from ten brands).
ALTER TABLE foods DROP CONSTRAINT foods_name_key;

ALTER TABLE foods
    ADD COLUMN source      text NOT NULL DEFAULT 'hxvy' CHECK (source IN ('hxvy', 'usda', 'off')),
    ADD COLUMN source_id   text,                -- USDA fdcId or the product barcode
    ADD COLUMN brand       text NOT NULL DEFAULT '',
    ADD COLUMN search_text text GENERATED ALWAYS AS (lower(name || ' ' || brand)) STORED;

-- Lets a re-import update a food instead of adding it twice.
CREATE UNIQUE INDEX foods_source_id_idx ON foods (source, source_id) WHERE source_id IS NOT NULL;

CREATE INDEX foods_search_idx ON foods USING gin (search_text gin_trgm_ops);
