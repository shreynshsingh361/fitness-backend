-- Indian dishes (dal tadka, idli, biryani...) from the Indian Nutrient Databank (INDB),
-- Vijayakumar et al., 2024, Creative Commons Attribution 4.0. Imported by scripts/import-foods.sh;
-- the app credits it. Source 'indb', source_id = the INDB food code (ASC001, BFP044...).
ALTER TABLE foods DROP CONSTRAINT foods_source_check;
ALTER TABLE foods ADD CONSTRAINT foods_source_check
    CHECK (source IN ('hxvy', 'usda', 'off', 'recipe', 'indb')) NOT VALID;
