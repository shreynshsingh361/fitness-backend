-- Recipes: a dish someone makes from foods in the catalogue ("Mom's dal": 200 g lentils,
-- 1 onion, 1 tbsp oil...). It makes a number of servings, and one serving's nutrition is
-- everything in it divided by that number.
--
-- A recipe is also a row in foods (source 'recipe'), private to the person who made it
-- (owner_id). So search finds it and the diary adds it like any other food: the entry
-- keeps a snapshot of one serving's nutrition, and changing or deleting the recipe later
-- never changes a day that was already logged.

-- NOT VALID: every existing row already passed the narrower check, so there is no need
-- to read all of them again. New and changed rows are still checked.
ALTER TABLE foods DROP CONSTRAINT foods_source_check;
ALTER TABLE foods ADD CONSTRAINT foods_source_check
    CHECK (source IN ('hxvy', 'usda', 'off', 'recipe')) NOT VALID;

-- Only recipes have an owner. As in the other tables, the user id is just a uuid: users
-- live in the auth service's database.
ALTER TABLE foods ADD COLUMN owner_id uuid;
ALTER TABLE foods ADD CONSTRAINT foods_recipe_owner_check
    CHECK ((source = 'recipe') = (owner_id IS NOT NULL)) NOT VALID;
CREATE INDEX foods_owner_idx ON foods (owner_id) WHERE owner_id IS NOT NULL;

CREATE TABLE recipes (
    food_id         uuid PRIMARY KEY REFERENCES foods(id) ON DELETE CASCADE,
    portions        integer          NOT NULL CHECK (portions BETWEEN 1 AND 100), -- servings it makes
    cooked_weight_g double precision CHECK (cooked_weight_g > 0), -- optional: lets people log it in grams
    created_at      timestamptz      NOT NULL DEFAULT now(),
    updated_at      timestamptz      NOT NULL DEFAULT now()
);

CREATE TABLE recipe_ingredients (
    recipe_id uuid             NOT NULL REFERENCES recipes(food_id) ON DELETE CASCADE,
    position  integer          NOT NULL, -- the order they were added in
    -- A catalogue food, never another recipe. No ON DELETE: a food used in a recipe
    -- cannot disappear from under it.
    food_id   uuid             NOT NULL REFERENCES foods(id),
    servings  double precision NOT NULL CHECK (servings > 0),
    PRIMARY KEY (recipe_id, position)
);

CREATE INDEX recipe_ingredients_food_idx ON recipe_ingredients (food_id);
