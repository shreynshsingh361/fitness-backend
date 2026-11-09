package store_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"fitness/nutrition-service/internal/store"
)

// A recipe's ingredients are saved in one statement and keep the order they were given in; saving
// the recipe again replaces them. Needs a throwaway Postgres (see testPool).
func TestRecipeIngredientsSaveInOrderAndReplace(t *testing.T) {
	pool := testPool(t)
	s := store.New(pool)
	ctx := context.Background()

	rows, err := pool.Query(ctx, `SELECT id FROM foods WHERE owner_id IS NULL ORDER BY name LIMIT 3`)
	if err != nil {
		t.Fatal(err)
	}
	var foods []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		foods = append(foods, id)
	}
	rows.Close()
	if len(foods) != 3 {
		t.Fatalf("the seed has %d foods, want at least 3", len(foods))
	}

	user := uuid.NewString()
	saved, err := s.SaveRecipe(ctx, user, "", store.RecipeInput{Name: "Dal rice", Portions: 2, Ingredients: []store.IngredientInput{
		{FoodID: foods[2], Servings: 1}, {FoodID: foods[0], Servings: 2.5}, {FoodID: foods[1], Servings: 0.5},
	}})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{foods[2], foods[0], foods[1]}
	if len(saved.Ingredients) != 3 {
		t.Fatalf("saved %d ingredients, want 3", len(saved.Ingredients))
	}
	for i, ing := range saved.Ingredients {
		if ing.Food.ID != want[i] {
			t.Errorf("ingredient %d is %s, want %s: the order must be kept", i, ing.Food.ID, want[i])
		}
	}
	if saved.Ingredients[1].Servings != 2.5 {
		t.Errorf("servings = %v, want 2.5", saved.Ingredients[1].Servings)
	}

	again, err := s.SaveRecipe(ctx, user, saved.ID, store.RecipeInput{Name: "Dal rice", Portions: 2, Ingredients: []store.IngredientInput{
		{FoodID: foods[1], Servings: 3},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Ingredients) != 1 || again.Ingredients[0].Food.ID != foods[1] {
		t.Fatalf("saving again must replace the ingredients, got %+v", again.Ingredients)
	}
}
