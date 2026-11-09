package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"fitness/nutrition-service/internal/store"
)

// ---------- recipes ----------

type recipeInput struct {
	Name          string   `json:"name"`
	Portions      int      `json:"portions"`
	CookedWeightG *float64 `json:"cooked_weight_g"` // optional
	Ingredients   []struct {
		FoodID   string  `json:"food_id"`
		Servings float64 `json:"servings"`
	} `json:"ingredients"`
}

func (h *handler) listRecipes(c *gin.Context) {
	list, err := h.store.Recipes(c.Request.Context(), userID(c))
	if err != nil {
		serverError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": list})
}

func (h *handler) getRecipe(c *gin.Context) {
	id, ok := recipeID(c)
	if !ok {
		return
	}
	recipe, err := h.store.Recipe(c.Request.Context(), userID(c), id)
	if errors.Is(err, store.ErrNotFound) {
		recipeNotFound(c)
		return
	}
	if err != nil {
		serverError(c, err)
		return
	}
	c.JSON(http.StatusOK, recipe)
}

func (h *handler) createRecipe(c *gin.Context) { h.saveRecipe(c, "") }

func (h *handler) updateRecipe(c *gin.Context) {
	if id, ok := recipeID(c); ok {
		h.saveRecipe(c, id)
	}
}

// saveRecipe creates a recipe (id "") or replaces the user's recipe with this id.
func (h *handler) saveRecipe(c *gin.Context, id string) {
	var in recipeInput
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, http.StatusBadRequest, "invalid_json", "request body must be valid JSON", nil)
		return
	}
	recipe, problems := validateRecipe(in)
	if len(problems) > 0 {
		fail(c, http.StatusUnprocessableEntity, "validation_error", "some fields are invalid", problems)
		return
	}

	saved, err := h.store.SaveRecipe(c.Request.Context(), userID(c), id, recipe)
	var unknown store.UnknownIngredientError
	switch {
	case errors.As(err, &unknown):
		fail(c, http.StatusUnprocessableEntity, "unknown_food", "an ingredient is not a food you can use",
			map[string]string{fmt.Sprintf("ingredients[%d].food_id", unknown.Index): "does not exist, or is a recipe"})
	case errors.Is(err, store.ErrNotFound):
		recipeNotFound(c)
	case err != nil:
		serverError(c, err)
	case id == "":
		c.JSON(http.StatusCreated, saved)
	default:
		c.JSON(http.StatusOK, saved)
	}
}

func (h *handler) deleteRecipe(c *gin.Context) {
	id, ok := recipeID(c)
	if !ok {
		return
	}
	err := h.store.DeleteRecipe(c.Request.Context(), userID(c), id)
	if errors.Is(err, store.ErrNotFound) {
		recipeNotFound(c)
		return
	}
	if err != nil {
		serverError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// recipeID reads the :id of the URL, and answers 404 itself when it is not a uuid.
func recipeID(c *gin.Context) (string, bool) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		recipeNotFound(c)
		return "", false
	}
	return id.String(), true
}

func recipeNotFound(c *gin.Context) {
	fail(c, http.StatusNotFound, "not_found", "recipe not found", nil)
}

// validateRecipe checks the request and returns it cleaned up: the name trimmed and the
// food ids in the database's own (lower-case) form.
func validateRecipe(in recipeInput) (store.RecipeInput, map[string]string) {
	problems := map[string]string{}
	out := store.RecipeInput{
		Name:          strings.TrimSpace(in.Name),
		Portions:      in.Portions,
		CookedWeightG: in.CookedWeightG,
	}

	if n := utf8.RuneCountInString(out.Name); n == 0 || n > store.MaxRecipeName {
		problems["name"] = fmt.Sprintf("is required, up to %d characters", store.MaxRecipeName)
	}
	if in.Portions < 1 || in.Portions > store.MaxRecipePortions {
		problems["portions"] = fmt.Sprintf("must be between 1 and %d", store.MaxRecipePortions)
	}
	if w := in.CookedWeightG; w != nil && (*w < 1 || *w > store.MaxCookedWeightG) {
		problems["cooked_weight_g"] = fmt.Sprintf("must be between 1 and %d grams, or left out", store.MaxCookedWeightG)
	}
	if n := len(in.Ingredients); n == 0 || n > store.MaxRecipeIngredients {
		problems["ingredients"] = fmt.Sprintf("must have between 1 and %d foods", store.MaxRecipeIngredients)
		return out, problems // no point checking each of 500 ingredients
	}

	for i, it := range in.Ingredients {
		id, err := uuid.Parse(it.FoodID)
		if err != nil {
			problems[fmt.Sprintf("ingredients[%d].food_id", i)] = "must be a uuid"
		}
		if it.Servings < 0.01 || it.Servings > store.MaxIngredientServings {
			problems[fmt.Sprintf("ingredients[%d].servings", i)] =
				fmt.Sprintf("must be between 0.01 and %d", store.MaxIngredientServings)
		}
		out.Ingredients = append(out.Ingredients, store.IngredientInput{FoodID: id.String(), Servings: it.Servings})
	}
	return out, problems
}
