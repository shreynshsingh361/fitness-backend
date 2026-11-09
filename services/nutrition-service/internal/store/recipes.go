package store

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

// ---------- recipes ----------
//
// A recipe is stored as a food of its own (source 'recipe', owner_id = its maker) plus a
// row in recipes and its ingredients. See migration 004 for why.

// Limits for a recipe. The API checks them before anything reaches the database.
const (
	MaxRecipeName         = 100
	MaxRecipePortions     = 100
	MaxRecipeIngredients  = 50
	MaxIngredientServings = 1000 // a big batch: 2 kg of rice is 20 servings of "100 g"
	MaxCookedWeightG      = 50000
)

// Ingredient is one food in a recipe and how many of its servings go in. Food holds the
// nutrition of ONE serving, so a screen can recalculate while the amount is changed.
type Ingredient struct {
	Food     Food    `json:"food"`
	Servings float64 `json:"servings"`
}

// Recipe is a dish made from foods. Its own nutrition is for one serving, and its id is
// also its food id: adding that id to the diary logs one serving of the recipe.
type Recipe struct {
	ID            string       `json:"id"`
	Name          string       `json:"name"`
	Portions      int          `json:"portions"`        // how many servings it makes
	CookedWeightG *float64     `json:"cooked_weight_g"` // the whole dish, if it was weighed
	Serving       string       `json:"serving"`         // "1 serving (300 g)" or "1 serving"
	Calories      int          `json:"calories"`
	ProteinG      float64      `json:"protein_g"`
	CarbsG        float64      `json:"carbs_g"`
	FatG          float64      `json:"fat_g"`
	Ingredients   []Ingredient `json:"ingredients"`
	UpdatedAt     time.Time    `json:"updated_at"`
}

type IngredientInput struct {
	FoodID   string // a lower-case uuid
	Servings float64
}

type RecipeInput struct {
	Name          string
	Portions      int
	CookedWeightG *float64
	Ingredients   []IngredientInput
}

// UnknownIngredientError says which ingredient (counting from 0) the recipe cannot use:
// it does not exist, or it is a recipe itself.
type UnknownIngredientError struct{ Index int }

func (e UnknownIngredientError) Error() string {
	return fmt.Sprintf("ingredient %d is not a catalogue food", e.Index)
}

const recipeColumns = `f.id, f.name, r.portions, r.cooked_weight_g, f.serving,
                       f.calories, f.protein_g, f.carbs_g, f.fat_g, r.updated_at`

// Recipes lists the user's recipes, by name.
func (s *Store) Recipes(ctx context.Context, userID string) ([]Recipe, error) {
	return s.recipes(ctx, `WHERE f.owner_id = $1 ORDER BY lower(f.name), f.id`, userID)
}

// Recipe returns one of the user's recipes, or ErrNotFound.
func (s *Store) Recipe(ctx context.Context, userID, id string) (Recipe, error) {
	list, err := s.recipes(ctx, `WHERE f.owner_id = $1 AND f.id = $2`, userID, id)
	if err != nil {
		return Recipe{}, err
	}
	if len(list) == 0 {
		return Recipe{}, ErrNotFound
	}
	return list[0], nil
}

func (s *Store) recipes(ctx context.Context, where string, args ...any) ([]Recipe, error) {
	rows, err := s.db.Query(ctx, `SELECT `+recipeColumns+` FROM foods f JOIN recipes r ON r.food_id = f.id `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	list := []Recipe{}
	for rows.Next() {
		r := Recipe{Ingredients: []Ingredient{}}
		if err := rows.Scan(&r.ID, &r.Name, &r.Portions, &r.CookedWeightG, &r.Serving,
			&r.Calories, &r.ProteinG, &r.CarbsG, &r.FatG, &r.UpdatedAt); err != nil {
			return nil, err
		}
		list = append(list, r)
	}
	if err := rows.Err(); err != nil || len(list) == 0 {
		return list, err
	}

	// Then every ingredient of all of them, in one query.
	ids := make([]string, len(list))
	at := make(map[string]int, len(list))
	for i, r := range list {
		ids[i], at[r.ID] = r.ID, i
	}
	rows, err = s.db.Query(ctx,
		`SELECT i.recipe_id, f.id, f.name, f.brand, f.source, f.serving, f.calories, f.protein_g, f.carbs_g, f.fat_g,
		        i.servings
		 FROM recipe_ingredients i JOIN foods f ON f.id = i.food_id
		 WHERE i.recipe_id = ANY($1::text[]::uuid[])
		 ORDER BY i.recipe_id, i.position`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var recipeID string
		var in Ingredient
		f := &in.Food
		if err := rows.Scan(&recipeID, &f.ID, &f.Name, &f.Brand, &f.Source, &f.Serving,
			&f.Calories, &f.ProteinG, &f.CarbsG, &f.FatG, &in.Servings); err != nil {
			return nil, err
		}
		r := &list[at[recipeID]]
		r.Ingredients = append(r.Ingredients, in)
	}
	return list, rows.Err()
}

// SaveRecipe creates a recipe (id "") or replaces one of the user's own, and returns it
// as saved. Errors: ErrNotFound (they have no recipe with that id) and
// UnknownIngredientError.
//
// Days on which the recipe was already logged keep the nutrition it had then.
func (s *Store) SaveRecipe(ctx context.Context, userID, id string, in RecipeInput) (Recipe, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Recipe{}, err
	}
	defer tx.Rollback(ctx)

	foods, err := catalogueFoods(ctx, tx, in.Ingredients)
	if err != nil {
		return Recipe{}, err
	}
	items := make([]Ingredient, len(in.Ingredients))
	for i, it := range in.Ingredients {
		items[i] = Ingredient{Food: foods[it.FoodID], Servings: it.Servings}
	}
	calories, protein, carbs, fat := recipeNutrition(items, in.Portions)
	serving := recipeServing(in.Portions, in.CookedWeightG)

	if id == "" {
		err = tx.QueryRow(ctx,
			`INSERT INTO foods (name, serving, calories, protein_g, carbs_g, fat_g, source, owner_id)
			 VALUES ($1, $2, $3, $4, $5, $6, 'recipe', $7) RETURNING id`,
			in.Name, serving, calories, protein, carbs, fat, userID).Scan(&id)
		if err != nil {
			return Recipe{}, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO recipes (food_id, portions, cooked_weight_g) VALUES ($1, $2, $3)`,
			id, in.Portions, in.CookedWeightG); err != nil {
			return Recipe{}, err
		}
	} else {
		tag, err := tx.Exec(ctx,
			`UPDATE foods SET name = $3, serving = $4, calories = $5, protein_g = $6, carbs_g = $7, fat_g = $8
			 WHERE id = $1 AND owner_id = $2`,
			id, userID, in.Name, serving, calories, protein, carbs, fat)
		if err != nil {
			return Recipe{}, err
		}
		if tag.RowsAffected() == 0 {
			return Recipe{}, ErrNotFound
		}
		for _, statement := range []struct {
			sql  string
			args []any
		}{
			{`UPDATE recipes SET portions = $2, cooked_weight_g = $3, updated_at = now() WHERE food_id = $1`,
				[]any{id, in.Portions, in.CookedWeightG}},
			{`DELETE FROM recipe_ingredients WHERE recipe_id = $1`, []any{id}},
		} {
			if _, err := tx.Exec(ctx, statement.sql, statement.args...); err != nil {
				return Recipe{}, err
			}
		}
	}

	if err := insertIngredients(ctx, tx, id, in.Ingredients); err != nil {
		return Recipe{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Recipe{}, err
	}
	return s.Recipe(ctx, userID, id)
}

// insertIngredients saves all of a recipe's ingredients in one statement, in the order given
// (position 0, 1, 2...). One round trip however many ingredients there are, instead of one each.
func insertIngredients(ctx context.Context, tx pgx.Tx, recipeID string, items []IngredientInput) error {
	foodIDs := make([]string, len(items))
	servings := make([]float64, len(items))
	for i, it := range items {
		foodIDs[i], servings[i] = it.FoodID, it.Servings
	}
	_, err := tx.Exec(ctx,
		`INSERT INTO recipe_ingredients (recipe_id, position, food_id, servings)
		 SELECT $1, ord - 1, food_id, servings
		 FROM unnest($2::uuid[], $3::float8[]) WITH ORDINALITY AS t(food_id, servings, ord)`,
		recipeID, foodIDs, servings)
	return err
}

// DeleteRecipe deletes one of the user's recipes with its ingredients. Days it was logged
// on keep their copy (the diary entry's food_id becomes null).
func (s *Store) DeleteRecipe(ctx context.Context, userID, id string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM foods WHERE id = $1 AND owner_id = $2`, id, userID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// catalogueFoods loads the food of every ingredient, by id. Only catalogue foods count
// (no owner), so a recipe can never contain a recipe, its own self included.
func catalogueFoods(ctx context.Context, tx pgx.Tx, items []IngredientInput) (map[string]Food, error) {
	ids := make([]string, len(items))
	for i, it := range items {
		ids[i] = it.FoodID
	}
	rows, err := tx.Query(ctx,
		`SELECT id, name, brand, source, serving, calories, protein_g, carbs_g, fat_g FROM foods
		 WHERE id = ANY($1::text[]::uuid[]) AND owner_id IS NULL`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	foods := map[string]Food{}
	for rows.Next() {
		var f Food
		if err := rows.Scan(&f.ID, &f.Name, &f.Brand, &f.Source, &f.Serving,
			&f.Calories, &f.ProteinG, &f.CarbsG, &f.FatG); err != nil {
			return nil, err
		}
		foods[f.ID] = f
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i, it := range items {
		if _, ok := foods[it.FoodID]; !ok {
			return nil, UnknownIngredientError{Index: i}
		}
	}
	return foods, nil
}

// recipeNutrition is one serving of a recipe: everything in it added up, then divided by
// the number of servings it makes. It rounds once, at the end, so small amounts of many
// ingredients do not lose their calories to rounding.
func recipeNutrition(items []Ingredient, portions int) (calories int, protein, carbs, fat float64) {
	var kcal float64
	for _, it := range items {
		kcal += float64(it.Food.Calories) * it.Servings
		protein += it.Food.ProteinG * it.Servings
		carbs += it.Food.CarbsG * it.Servings
		fat += it.Food.FatG * it.Servings
	}
	n := float64(portions)
	return int(math.Round(kcal / n)), round1(protein / n), round1(carbs / n), round1(fat / n)
}

// recipeServing is the label of one serving. With the cooked weight it says what one
// serving weighs, "1 serving (300 g)", and the app can then log the recipe in grams.
func recipeServing(portions int, cookedWeightG *float64) string {
	if cookedWeightG != nil {
		if each := round1(*cookedWeightG / float64(portions)); each > 0 {
			return "1 serving (" + strconv.FormatFloat(each, 'f', -1, 64) + " g)"
		}
	}
	return "1 serving"
}
