package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"fitness/workout-service/internal/store"
)

type handler struct {
	store *store.Store
}

// NewRouter lists every request this service accepts.
func NewRouter(s *store.Store) *gin.Engine {
	h := &handler{store: s}

	r := gin.New()
	r.Use(gin.Recovery(), requestLog())

	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok", "service": "workout-service"})
	})
	r.GET("/ready", readiness("workout-service", s))

	v1 := r.Group("/api/v1", requireUser())
	v1.GET("/exercises", h.listExercises)
	v1.POST("/workouts", h.createWorkout)
	v1.GET("/workouts", h.listWorkouts)
	v1.GET("/workouts/summary", h.summary)
	v1.GET("/workouts/:id", h.getWorkout)
	v1.DELETE("/workouts/:id", h.deleteWorkout)
	v1.GET("/records", h.records)

	v1.GET("/plans", h.planSummaries)
	v1.GET("/plans/:part", h.getPlan)
	v1.POST("/plans/:part/exercises", h.addToPlan)
	v1.PUT("/plans/:part/exercises/:id", h.updatePlanExercise)
	v1.DELETE("/plans/:part/exercises/:id", h.removeFromPlan)

	// For other services only. The gateway has no route to /internal, so this
	// cannot be reached from outside (see the gateway's route table).
	r.DELETE("/internal/users/:id", h.deleteUser)
	return r
}

// deleteUser removes everything this service knows about a person. It is called
// when an account is deleted, and is safe to repeat.
func (h *handler) deleteUser(c *gin.Context) {
	id := c.Param("id")
	if _, err := uuid.Parse(id); err != nil {
		fail(c, http.StatusNotFound, "not_found", "no such user", nil)
		return
	}
	if err := h.store.DeleteUser(c.Request.Context(), id); err != nil {
		serverError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *handler) listExercises(c *gin.Context) {
	pageNum, limit, offset := pageParams(c)
	items, total, err := h.store.ListExercises(c.Request.Context(),
		strings.TrimSpace(c.Query("search")), strings.TrimSpace(c.Query("muscle_group")),
		strings.TrimSpace(c.Query("body_part")), limit, offset)
	if err != nil {
		serverError(c, err)
		return
	}
	c.JSON(http.StatusOK, page[store.Exercise]{Items: items, Page: pageNum, Limit: limit, Total: total})
}

func (h *handler) createWorkout(c *gin.Context) {
	var in store.NewWorkout
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, http.StatusBadRequest, "invalid_json", "request body must be valid JSON", nil)
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	in.Notes = strings.TrimSpace(in.Notes)
	if problems := validateWorkout(in); len(problems) > 0 {
		fail(c, http.StatusUnprocessableEntity, "validation_error", "some fields are invalid", problems)
		return
	}

	w, created, err := h.store.CreateWorkout(c.Request.Context(), userID(c), in)
	if errors.Is(err, store.ErrUnknownExercise) {
		fail(c, http.StatusUnprocessableEntity, "unknown_exercise", "one of the exercise_id values does not exist", nil)
		return
	}
	if err != nil {
		serverError(c, err)
		return
	}
	// 201 = newly created. 200 = we had already saved this client_op_id (a retry).
	status := http.StatusCreated
	if !created {
		status = http.StatusOK
	}
	c.JSON(status, w)
}

func (h *handler) listWorkouts(c *gin.Context) {
	pageNum, limit, offset := pageParams(c)
	items, total, err := h.store.ListWorkouts(c.Request.Context(), userID(c), limit, offset)
	if err != nil {
		serverError(c, err)
		return
	}
	c.JSON(http.StatusOK, page[store.Workout]{Items: items, Page: pageNum, Limit: limit, Total: total})
}

func (h *handler) getWorkout(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	w, err := h.store.GetWorkout(c.Request.Context(), userID(c), id)
	if errors.Is(err, store.ErrNotFound) {
		fail(c, http.StatusNotFound, "not_found", "workout not found", nil)
		return
	}
	if err != nil {
		serverError(c, err)
		return
	}
	c.JSON(http.StatusOK, w)
}

func (h *handler) deleteWorkout(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	err := h.store.DeleteWorkout(c.Request.Context(), userID(c), id)
	if errors.Is(err, store.ErrNotFound) {
		fail(c, http.StatusNotFound, "not_found", "workout not found", nil)
		return
	}
	if err != nil {
		serverError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *handler) summary(c *gin.Context) {
	days := intQuery(c, "days", 7)
	if days < 1 || days > 365 {
		fail(c, http.StatusUnprocessableEntity, "validation_error", "days must be between 1 and 365", nil)
		return
	}
	sum, err := h.store.Summary(c.Request.Context(), userID(c), days)
	if err != nil {
		serverError(c, err)
		return
	}
	c.JSON(http.StatusOK, sum)
}

func (h *handler) records(c *gin.Context) {
	records, err := h.store.PersonalRecords(c.Request.Context(), userID(c))
	if err != nil {
		serverError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": records})
}

// pathID reads :id and answers 404 itself if it is not a valid uuid.
func pathID(c *gin.Context) (string, bool) {
	id := c.Param("id")
	if _, err := uuid.Parse(id); err != nil {
		fail(c, http.StatusNotFound, "not_found", "workout not found", nil)
		return "", false
	}
	return id, true
}

// validateWorkout returns one message per bad field (empty map = all good).
func validateWorkout(in store.NewWorkout) map[string]string {
	problems := map[string]string{}
	if _, err := uuid.Parse(in.ClientOpID); err != nil {
		problems["client_op_id"] = "is required and must be a uuid you generate once per workout"
	}
	if in.Name == "" || len(in.Name) > 100 {
		problems["name"] = "is required and must be at most 100 characters"
	}
	if len(in.Notes) > 1000 {
		problems["notes"] = "must be at most 1000 characters"
	}
	if in.DurationMinutes < 0 || in.DurationMinutes > 1440 {
		problems["duration_minutes"] = "must be between 0 and 1440"
	}
	if len(in.Sets) == 0 || len(in.Sets) > 100 {
		problems["sets"] = "must contain between 1 and 100 sets"
	}
	for i, set := range in.Sets {
		if _, err := uuid.Parse(set.ExerciseID); err != nil {
			problems[fmt.Sprintf("sets[%d].exercise_id", i)] = "must be a uuid"
		}
		if set.Reps < 1 || set.Reps > 1000 {
			problems[fmt.Sprintf("sets[%d].reps", i)] = "must be between 1 and 1000"
		}
		if set.WeightKg < 0 || set.WeightKg > 1000 {
			problems[fmt.Sprintf("sets[%d].weight_kg", i)] = "must be between 0 and 1000"
		}
	}
	return problems
}
