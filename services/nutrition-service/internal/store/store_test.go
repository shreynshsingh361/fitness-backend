package store

import "testing"

func TestScaleFood(t *testing.T) {
	egg := Food{Calories: 72, ProteinG: 6.3, CarbsG: 0.4, FatG: 4.8}

	cases := []struct {
		servings            float64
		calories            int
		protein, carbs, fat float64
	}{
		{1, 72, 6.3, 0.4, 4.8},
		{2, 144, 12.6, 0.8, 9.6},
		{0.5, 36, 3.2, 0.2, 2.4}, // 3.15 rounds to 3.2, 0.2 stays, 2.4 stays
		{3, 216, 18.9, 1.2, 14.4},
	}
	for _, tc := range cases {
		cal, p, c, f := scaleFood(egg, tc.servings)
		if cal != tc.calories || p != tc.protein || c != tc.carbs || f != tc.fat {
			t.Errorf("servings=%v: got %d kcal P%v C%v F%v, want %d kcal P%v C%v F%v",
				tc.servings, cal, p, c, f, tc.calories, tc.protein, tc.carbs, tc.fat)
		}
	}
}

func TestRound1(t *testing.T) {
	if got := round1(12.349); got != 12.3 {
		t.Errorf("round1(12.349) = %v, want 12.3", got)
	}
	if got := round1(12.35); got != 12.4 && got != 12.3 { // float representation of x.x5 can go either way
		t.Errorf("round1(12.35) = %v, want 12.3 or 12.4", got)
	}
}

func TestRecipeNutritionIsPerServing(t *testing.T) {
	lentils := Food{Calories: 116, ProteinG: 9, CarbsG: 20.1, FatG: 0.4} // per 100 g
	oil := Food{Calories: 119, ProteinG: 0, CarbsG: 0, FatG: 13.5}       // per tbsp
	dal := []Ingredient{{Food: lentils, Servings: 3}, {Food: oil, Servings: 1}}

	// All of it: 348 + 119 = 467 kcal, 27 g protein, 60.3 g carbs, 13.5 + 1.2 = 14.7 g fat.
	cal, p, c, f := recipeNutrition(dal, 1)
	if cal != 467 || p != 27 || c != 60.3 || f != 14.7 {
		t.Errorf("whole recipe: got %d kcal P%v C%v F%v", cal, p, c, f)
	}
	// Split in 4: 116.75 kcal rounds to 117.
	cal, p, c, f = recipeNutrition(dal, 4)
	if cal != 117 || p != 6.8 || c != 15.1 || f != 3.7 {
		t.Errorf("one of 4 servings: got %d kcal P%v C%v F%v", cal, p, c, f)
	}
}

func TestRecipeNutritionRoundsOnlyAtTheEnd(t *testing.T) {
	// Ten pinches of 0.4 kcal would be ten zeros if each were rounded first.
	pinch := Food{Calories: 4}
	items := make([]Ingredient, 10)
	for i := range items {
		items[i] = Ingredient{Food: pinch, Servings: 0.1}
	}
	if cal, _, _, _ := recipeNutrition(items, 1); cal != 4 {
		t.Errorf("calories = %d, want 4", cal)
	}
}

func TestRecipeServingLabel(t *testing.T) {
	weight := func(g float64) *float64 { return &g }
	cases := []struct {
		portions int
		weight   *float64
		want     string
	}{
		{4, nil, "1 serving"},
		{4, weight(1200), "1 serving (300 g)"},
		{3, weight(1000), "1 serving (333.3 g)"},
		{100, weight(1), "1 serving"}, // 0.01 g each rounds to nothing: better no weight than "0 g"
	}
	for _, tc := range cases {
		if got := recipeServing(tc.portions, tc.weight); got != tc.want {
			t.Errorf("recipeServing(%d, %v) = %q, want %q", tc.portions, tc.weight, got, tc.want)
		}
	}
}
