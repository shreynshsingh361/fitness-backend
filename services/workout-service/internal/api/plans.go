package api

import (
	"errors"
	"fmt"
	"net/http"
	"slices"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"fitness/workout-service/internal/store"
)

// ---------- plans ----------

func (h *handler) planSummaries(c *gin.Context) {
	items, err := h.store.PlanSummaries(c.Request.Context(), userID(c))
	if err != nil {
		serverError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

func (h *handler) getPlan(c *gin.Context) {
	part, ok := bodyPart(c)
	if !ok {
		return
	}
	items, err := h.store.Plan(c.Request.Context(), userID(c), part)
	if err != nil {
		serverError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"body_part": part, "exercises": items})
}

func (h *handler) addToPlan(c *gin.Context) {
	part, ok := bodyPart(c)
	if !ok {
		return
	}
	var in struct {
		ExerciseID string `json:"exercise_id"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, http.StatusBadRequest, "invalid_json", "request body must be valid JSON", nil)
		return
	}
	if _, err := uuid.Parse(in.ExerciseID); err != nil {
		fail(c, http.StatusUnprocessableEntity, "validation_error", "some fields are invalid",
			map[string]string{"exercise_id": "is required and must be a uuid"})
		return
	}

	added, err := h.store.AddToPlan(c.Request.Context(), userID(c), part, in.ExerciseID)
	switch {
	case errors.Is(err, store.ErrUnknownExercise):
		fail(c, http.StatusUnprocessableEntity, "unknown_exercise", "exercise_id does not exist", nil)
	case errors.Is(err, store.ErrWrongBodyPart):
		fail(c, http.StatusUnprocessableEntity, "wrong_body_part", "that exercise is not for "+part, nil)
	case errors.Is(err, store.ErrAlreadyInPlan):
		fail(c, http.StatusConflict, "already_in_plan", "that exercise is already in your plan", nil)
	case errors.Is(err, store.ErrPlanFull):
		fail(c, http.StatusUnprocessableEntity, "plan_full",
			fmt.Sprintf("a plan can have at most %d exercises", store.MaxPlanExercises), nil)
	case err != nil:
		serverError(c, err)
	default:
		c.JSON(http.StatusCreated, added)
	}
}

type planExerciseInput struct {
	Unit string          `json:"unit"`
	Sets []store.PlanSet `json:"sets"`
}

func (h *handler) updatePlanExercise(c *gin.Context) {
	part, id, ok := planExerciseID(c)
	if !ok {
		return
	}
	var in planExerciseInput
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, http.StatusBadRequest, "invalid_json", "request body must be valid JSON", nil)
		return
	}
	if problems := validatePlanExercise(in); len(problems) > 0 {
		fail(c, http.StatusUnprocessableEntity, "validation_error", "some fields are invalid", problems)
		return
	}
	saved, err := h.store.UpdatePlanExercise(c.Request.Context(), userID(c), part, id, in.Unit, in.Sets)
	if errors.Is(err, store.ErrNotFound) {
		fail(c, http.StatusNotFound, "not_found", "exercise not in your plan", nil)
		return
	}
	if err != nil {
		serverError(c, err)
		return
	}
	c.JSON(http.StatusOK, saved)
}

func (h *handler) removeFromPlan(c *gin.Context) {
	part, id, ok := planExerciseID(c)
	if !ok {
		return
	}
	err := h.store.RemoveFromPlan(c.Request.Context(), userID(c), part, id)
	if errors.Is(err, store.ErrNotFound) {
		fail(c, http.StatusNotFound, "not_found", "exercise not in your plan", nil)
		return
	}
	if err != nil {
		serverError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// bodyPart reads :part, and answers 404 itself when there is no such body part.
func bodyPart(c *gin.Context) (string, bool) {
	part := c.Param("part")
	if !slices.Contains(store.BodyParts, part) {
		fail(c, http.StatusNotFound, "not_found", "no such body part", nil)
		return "", false
	}
	return part, true
}

func planExerciseID(c *gin.Context) (part, id string, ok bool) {
	if part, ok = bodyPart(c); !ok {
		return "", "", false
	}
	parsed, err := uuid.Parse(c.Param("id"))
	if err != nil {
		fail(c, http.StatusNotFound, "not_found", "exercise not in your plan", nil)
		return "", "", false
	}
	return part, parsed.String(), true
}

// The heaviest weight a plan set may have, the same for every exercise.
const (
	maxWeightKg = 250.0
	maxWeightLb = 551.0
)

func unitOr(unit string) string {
	if unit == "lb" {
		return "lb"
	}
	return "kg"
}

func validatePlanExercise(in planExerciseInput) map[string]string {
	problems := map[string]string{}
	if in.Unit != "kg" && in.Unit != "lb" {
		problems["unit"] = "must be kg or lb"
	}
	if len(in.Sets) < 1 || len(in.Sets) > store.MaxPlanSets {
		problems["sets"] = fmt.Sprintf("must have between 1 and %d sets", store.MaxPlanSets)
		return problems
	}
	// No exercise goes above 250 kg; in pounds that is 551 lb, what the app shows for 250 kg.
	maxWeight := maxWeightKg
	if in.Unit == "lb" {
		maxWeight = maxWeightLb
	}
	for i, set := range in.Sets {
		if set.Weight < 0 || set.Weight > maxWeight {
			problems[fmt.Sprintf("sets[%d].weight", i)] = fmt.Sprintf("must be between 0 and %g %s", maxWeight, unitOr(in.Unit))
		}
		if set.Reps < 1 || set.Reps > 1000 {
			problems[fmt.Sprintf("sets[%d].reps", i)] = "must be between 1 and 1000"
		}
	}
	return problems
}
