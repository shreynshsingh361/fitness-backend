package api

import (
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"fitness/nutrition-service/internal/store"
)

const dateLayout = "2006-01-02"

var meals = map[string]bool{"breakfast": true, "lunch": true, "dinner": true, "snack": true}

type handler struct {
	store *store.Store
}

// NewRouter lists every request this service accepts.
func NewRouter(s *store.Store) *gin.Engine {
	h := &handler{store: s}

	r := gin.New()
	r.Use(gin.Recovery(), requestLog())

	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok", "service": "nutrition-service"})
	})
	r.GET("/ready", readiness("nutrition-service", s))

	v1 := r.Group("/api/v1", requireUser())
	v1.GET("/foods", h.listFoods)
	v1.GET("/foods/barcode/:code", h.foodByBarcode)

	v1.GET("/recipes", h.listRecipes)
	v1.POST("/recipes", h.createRecipe)
	v1.GET("/recipes/:id", h.getRecipe)
	v1.PUT("/recipes/:id", h.updateRecipe)
	v1.DELETE("/recipes/:id", h.deleteRecipe)

	v1.POST("/diary", h.createEntry)
	v1.GET("/diary", h.getDiary)
	v1.GET("/diary/month", h.getMonth)
	v1.DELETE("/diary/:id", h.deleteEntry)

	v1.GET("/goals", h.getGoal)
	v1.PUT("/goals", h.setGoal)

	v1.POST("/water", h.addWater)
	v1.GET("/water", h.getWater)
	v1.DELETE("/water", h.clearWater)

	v1.GET("/nutrition/summary", h.summary)

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

// ---------- foods ----------

func (h *handler) listFoods(c *gin.Context) {
	pageNum, limit, offset := pageParams(c)
	items, total, err := h.store.ListFoods(c.Request.Context(), userID(c), strings.TrimSpace(c.Query("search")),
		limit, offset)
	if err != nil {
		serverError(c, err)
		return
	}
	c.JSON(http.StatusOK, page[store.Food]{Items: items, Page: pageNum, Limit: limit, Total: total})
}

// ---------- diary ----------

type createEntryInput struct {
	ClientOpID string  `json:"client_op_id"`
	FoodID     string  `json:"food_id"`
	Meal       string  `json:"meal"`
	Servings   float64 `json:"servings"`
	EatenOn    string  `json:"eaten_on"` // YYYY-MM-DD, optional (defaults to today, UTC)
}

func (h *handler) createEntry(c *gin.Context) {
	var in createEntryInput
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, http.StatusBadRequest, "invalid_json", "request body must be valid JSON", nil)
		return
	}
	day, problems := validateEntry(in)
	if len(problems) > 0 {
		fail(c, http.StatusUnprocessableEntity, "validation_error", "some fields are invalid", problems)
		return
	}

	entry, created, err := h.store.CreateEntry(c.Request.Context(), userID(c), store.NewEntry{
		ClientOpID: in.ClientOpID,
		FoodID:     in.FoodID,
		Meal:       in.Meal,
		Servings:   in.Servings,
		EatenOn:    day,
	})
	if errors.Is(err, store.ErrUnknownFood) {
		fail(c, http.StatusUnprocessableEntity, "unknown_food", "food_id does not exist", nil)
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
	c.JSON(status, entry)
}

func (h *handler) getDiary(c *gin.Context) {
	day, ok := queryDay(c)
	if !ok {
		return
	}
	entries, totals, err := h.store.DiaryForDay(c.Request.Context(), userID(c), day)
	if err != nil {
		serverError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"date": day.Format(dateLayout), "entries": entries, "totals": totals})
}

// foodByBarcode finds the packaged product a scanned barcode belongs to: first in the
// catalogue, then (once) at Open Food Facts, which is kept for next time.
func (h *handler) foodByBarcode(c *gin.Context) {
	code := c.Param("code")
	if !validBarcode(code) {
		fail(c, http.StatusUnprocessableEntity, "validation_error", "some fields are invalid",
			map[string]string{"code": "must be a barcode of 8 to 14 digits"})
		return
	}
	ctx := c.Request.Context()
	food, found, err := h.store.FoodByBarcode(ctx, code)
	if err != nil {
		serverError(c, err)
		return
	}
	if found {
		c.JSON(http.StatusOK, food)
		return
	}

	fetched, err := openFoodFacts(ctx, code)
	switch {
	case errors.Is(err, errNoNutrition):
		fail(c, http.StatusNotFound, "no_nutrition", "that product has no nutrition information yet", nil)
		return
	case err != nil:
		// Open Food Facts being slow or down is not the user's fault, and not a missing product.
		log.Printf("req=%s open food facts: %v", c.GetHeader("X-Request-ID"), err)
		fail(c, http.StatusBadGateway, "lookup_unavailable", "could not look that product up right now", nil)
		return
	case fetched == nil:
		fail(c, http.StatusNotFound, "not_found", "no product with that barcode", nil)
		return
	}
	saved, err := h.store.SaveScannedFood(ctx, code, *fetched)
	if err != nil {
		serverError(c, err)
		return
	}
	c.JSON(http.StatusOK, saved)
}

// validBarcode is true for 8 to 14 digits: EAN-8, UPC-A, EAN-13 and ITF-14.
func validBarcode(code string) bool {
	if len(code) < 8 || len(code) > 14 {
		return false
	}
	for _, r := range code {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// getMonth answers with the calories eaten on each day of ?month=2026-11 (days with no food
// are left out), so the calendar can show them without asking for every day.
func (h *handler) getMonth(c *gin.Context) {
	first, err := time.Parse("2006-01", c.Query("month"))
	if err != nil {
		fail(c, http.StatusUnprocessableEntity, "validation_error", "some fields are invalid",
			map[string]string{"month": "must look like 2026-11"})
		return
	}
	days, err := h.store.CaloriesInMonth(c.Request.Context(), userID(c), first)
	if err != nil {
		serverError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"month": first.Format("2006-01"), "days": days})
}

func (h *handler) deleteEntry(c *gin.Context) {
	id := c.Param("id")
	if _, err := uuid.Parse(id); err != nil {
		fail(c, http.StatusNotFound, "not_found", "diary entry not found", nil)
		return
	}
	err := h.store.DeleteEntry(c.Request.Context(), userID(c), id)
	if errors.Is(err, store.ErrNotFound) {
		fail(c, http.StatusNotFound, "not_found", "diary entry not found", nil)
		return
	}
	if err != nil {
		serverError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// ---------- goals ----------

func (h *handler) getGoal(c *gin.Context) {
	calories, isDefault, err := h.store.CalorieGoal(c.Request.Context(), userID(c))
	if err != nil {
		serverError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"calories": calories, "is_default": isDefault})
}

func (h *handler) setGoal(c *gin.Context) {
	var in struct {
		Calories int `json:"calories"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, http.StatusBadRequest, "invalid_json", "request body must be valid JSON", nil)
		return
	}
	if in.Calories < 500 || in.Calories > 10000 {
		fail(c, http.StatusUnprocessableEntity, "validation_error", "some fields are invalid",
			map[string]string{"calories": "must be between 500 and 10000"})
		return
	}
	if err := h.store.SetCalorieGoal(c.Request.Context(), userID(c), in.Calories); err != nil {
		serverError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"calories": in.Calories, "is_default": false})
}

// ---------- water ----------

func (h *handler) addWater(c *gin.Context) {
	var in struct {
		AmountMl   int    `json:"amount_ml"`
		Date       string `json:"date"`         // YYYY-MM-DD, optional
		ClientOpID string `json:"client_op_id"` // optional uuid; a request sent twice is applied once
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, http.StatusBadRequest, "invalid_json", "request body must be valid JSON", nil)
		return
	}
	problems := map[string]string{}
	if in.AmountMl < 1 || in.AmountMl > 5000 {
		problems["amount_ml"] = "must be between 1 and 5000"
	}
	day, err := parseDay(in.Date)
	if err != nil {
		problems["date"] = "must look like 2026-11-07"
	}
	if in.ClientOpID != "" {
		if _, err := uuid.Parse(in.ClientOpID); err != nil {
			problems["client_op_id"] = "must be a uuid"
		}
	}
	if len(problems) > 0 {
		fail(c, http.StatusUnprocessableEntity, "validation_error", "some fields are invalid", problems)
		return
	}

	if err := h.store.AddWater(c.Request.Context(), userID(c), day, in.AmountMl, in.ClientOpID); err != nil {
		serverError(c, err)
		return
	}
	h.respondWater(c, http.StatusCreated, day)
}

func (h *handler) getWater(c *gin.Context) {
	day, ok := queryDay(c)
	if !ok {
		return
	}
	h.respondWater(c, http.StatusOK, day)
}

// clearWater resets the day's water, or with ?amount_ml= takes only that much off it.
func (h *handler) clearWater(c *gin.Context) {
	day, ok := queryDay(c)
	if !ok {
		return
	}
	var err error
	if raw, given := c.GetQuery("amount_ml"); given {
		// A bad amount is refused; it must never fall through to wiping the whole day.
		ml, convErr := strconv.Atoi(raw)
		if convErr != nil || ml < 1 || ml > 5000 {
			fail(c, http.StatusUnprocessableEntity, "validation_error", "some fields are invalid",
				map[string]string{"amount_ml": "must be between 1 and 5000"})
			return
		}
		opID := c.Query("client_op_id")
		if _, parseErr := uuid.Parse(opID); opID != "" && parseErr != nil {
			fail(c, http.StatusUnprocessableEntity, "validation_error", "some fields are invalid",
				map[string]string{"client_op_id": "must be a uuid"})
			return
		}
		err = h.store.RemoveWater(c.Request.Context(), userID(c), day, ml, opID)
	} else {
		err = h.store.ClearWater(c.Request.Context(), userID(c), day)
	}
	if err != nil {
		serverError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *handler) respondWater(c *gin.Context, status int, day time.Time) {
	total, err := h.store.WaterTotal(c.Request.Context(), userID(c), day)
	if err != nil {
		serverError(c, err)
		return
	}
	c.JSON(status, gin.H{"date": day.Format(dateLayout), "water_ml": total})
}

// ---------- summary ----------

func (h *handler) summary(c *gin.Context) {
	day, ok := queryDay(c)
	if !ok {
		return
	}
	sum, err := h.store.Summary(c.Request.Context(), userID(c), day)
	if err != nil {
		serverError(c, err)
		return
	}
	c.JSON(http.StatusOK, sum)
}

// ---------- helpers ----------

// parseDay turns "2026-11-07" into a date. An empty string means today (UTC).
// The browser always sends its own local date, because "today" depends on the
// user's time zone and the server does not know it.
func parseDay(s string) (time.Time, error) {
	if s == "" {
		now := time.Now().UTC()
		return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC), nil
	}
	return time.Parse(dateLayout, s)
}

// queryDay reads ?date= and answers 422 itself if it is malformed.
func queryDay(c *gin.Context) (time.Time, bool) {
	day, err := parseDay(c.Query("date"))
	if err != nil {
		fail(c, http.StatusUnprocessableEntity, "validation_error", "some fields are invalid",
			map[string]string{"date": "must look like 2026-11-07"})
		return time.Time{}, false
	}
	return day, true
}

// validateEntry checks the request and returns the parsed date.
func validateEntry(in createEntryInput) (time.Time, map[string]string) {
	problems := map[string]string{}
	if _, err := uuid.Parse(in.ClientOpID); err != nil {
		problems["client_op_id"] = "is required and must be a uuid you generate once per entry"
	}
	if _, err := uuid.Parse(in.FoodID); err != nil {
		problems["food_id"] = "is required and must be a uuid"
	}
	if !meals[in.Meal] {
		problems["meal"] = "must be breakfast, lunch, dinner or snack"
	}
	if in.Servings < 0.01 || in.Servings > 100 {
		problems["servings"] = "must be between 0.01 and 100"
	}
	day, err := parseDay(in.EatenOn)
	if err != nil {
		problems["eaten_on"] = "must look like 2026-11-07"
	}
	return day, problems
}
