package api

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"fitness/activity-service/internal/stats"
	"fitness/activity-service/internal/store"
)

const (
	dateLayout = "2006-01-02"

	maxDeviceSteps  = 100000 // more than a person walks in a day (ultra-marathon days included): a limit on bad data and cheating
	maxSyncDays     = 60     // how many days one sync may carry
	maxSyncBackDays = 3660   // how far back a day may be: 10 years of history from the health apps
	maxHistoryDays  = 366
	maxTotalsUsers  = 20000 // a group holds at most 15,000 people
)

// The sources a device sync may claim. Steps only ever come from a device or a health
// app: nobody can type them in.
var deviceSources = map[string]bool{
	"phone":          true,
	"apple_health":   true,
	"health_connect": true,
	"fitbit":         true,
	"garmin":         true,
	"samsung":        true,
}

var countryCode = regexp.MustCompile(`^[A-Z]{2}$`)

type handler struct {
	store *store.Store
}

// NewRouter lists every request this service accepts.
func NewRouter(s *store.Store) *gin.Engine {
	h := &handler{store: s}

	r := gin.New()
	r.Use(gin.Recovery(), requestLog())

	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok", "service": "activity-service"})
	})
	r.GET("/ready", readiness("activity-service", s))

	steps := r.Group("/api/v1/steps", requireUser())
	steps.POST("/sync", h.sync)
	steps.GET("/today", h.today)
	steps.GET("/history", h.history)
	steps.GET("/stats", h.stats)
	steps.GET("/settings", h.getSettings)
	steps.PUT("/settings", h.putSettings)

	// For other services only. The gateway has no route to /internal, so these
	// cannot be reached from outside (see the gateway's route table).
	internal := r.Group("/internal")
	internal.POST("/steps/totals", h.totals)
	internal.DELETE("/users/:id", h.deleteUser)
	return r
}

// ---------- saving steps ----------

type syncInput struct {
	Source string `json:"source"`
	Days   []struct {
		Date  string `json:"date"`
		Steps int    `json:"steps"`
	} `json:"days"`
}

func (h *handler) sync(c *gin.Context) {
	var in syncInput
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, http.StatusBadRequest, "invalid_json", "request body must be valid JSON", nil)
		return
	}
	days, problems := validateSync(in, time.Now())
	if len(problems) > 0 {
		invalid(c, problems)
		return
	}
	if err := h.store.Sync(c.Request.Context(), userID(c), in.Source, days); err != nil {
		serverError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"synced": len(days)})
}

func validateSync(in syncInput, now time.Time) ([]store.DayCount, map[string]string) {
	problems := map[string]string{}
	if !deviceSources[in.Source] {
		problems["source"] = "must be one of phone, apple_health, health_connect, fitbit, garmin, samsung"
	}
	if len(in.Days) == 0 || len(in.Days) > maxSyncDays {
		problems["days"] = fmt.Sprintf("must contain between 1 and %d days", maxSyncDays)
	}
	days := make([]store.DayCount, 0, len(in.Days))
	for i, d := range in.Days {
		day, err := checkDay(d.Date, maxSyncBackDays, now)
		if err != nil {
			problems[fmt.Sprintf("days[%d].date", i)] = err.Error()
		}
		if d.Steps < 0 || d.Steps > maxDeviceSteps {
			problems[fmt.Sprintf("days[%d].steps", i)] = fmt.Sprintf("must be between 0 and %d", maxDeviceSteps)
		}
		days = append(days, store.DayCount{Day: day, Steps: d.Steps})
	}
	return days, problems
}

// ---------- reading steps ----------

func (h *handler) today(c *gin.Context) {
	day, ok := queryDay(c, "date")
	if !ok {
		return
	}
	h.respondDay(c, day)
}

type dayResponse struct {
	Date          string              `json:"date"`
	Steps         int                 `json:"steps"`
	Goal          int                 `json:"goal"`
	GoalMet       bool                `json:"goal_met"`
	DistanceKm    float64             `json:"distance_km"`
	Calories      int                 `json:"calories"`
	Sources       []store.SourceSteps `json:"sources"`
	CurrentStreak int                 `json:"current_streak"`
}

func (h *handler) respondDay(c *gin.Context, day time.Time) {
	ctx := c.Request.Context()
	settings, err := h.store.Settings(ctx, userID(c))
	if err != nil {
		serverError(c, err)
		return
	}
	days, err := h.store.DailyTotals(ctx, userID(c))
	if err != nil {
		serverError(c, err)
		return
	}
	sources, err := h.store.Sources(ctx, userID(c), day)
	if err != nil {
		serverError(c, err)
		return
	}

	steps := 0
	for _, d := range days {
		if d.Date.Equal(day) {
			steps = d.Steps
		}
	}
	current, _ := stats.Streaks(days, settings.DailyGoal, day)
	c.JSON(http.StatusOK, dayResponse{
		Date:          day.Format(dateLayout),
		Steps:         steps,
		Goal:          settings.DailyGoal,
		GoalMet:       steps >= settings.DailyGoal,
		DistanceKm:    stats.DistanceKm(steps),
		Calories:      stats.Calories(steps),
		Sources:       sources,
		CurrentStreak: current,
	})
}

type historyDay struct {
	Date       string  `json:"date"`
	Steps      int     `json:"steps"`
	GoalMet    bool    `json:"goal_met"`
	DistanceKm float64 `json:"distance_km"`
	Calories   int     `json:"calories"`
}

func (h *handler) history(c *gin.Context) {
	to, ok := queryDay(c, "to")
	if !ok {
		return
	}
	from := to.AddDate(0, 0, -6)
	if c.Query("from") != "" {
		if from, ok = queryDay(c, "from"); !ok {
			return
		}
	}
	if from.After(to) {
		invalid(c, map[string]string{"from": "must not be after to"})
		return
	}
	if int(to.Sub(from).Hours()/24)+1 > maxHistoryDays {
		invalid(c, map[string]string{"from": fmt.Sprintf("the range can be at most %d days", maxHistoryDays)})
		return
	}

	ctx := c.Request.Context()
	settings, err := h.store.Settings(ctx, userID(c))
	if err != nil {
		serverError(c, err)
		return
	}
	totals, err := h.store.DailyTotals(ctx, userID(c))
	if err != nil {
		serverError(c, err)
		return
	}
	c.JSON(http.StatusOK, buildHistory(totals, settings.DailyGoal, from, to))
}

type historyResponse struct {
	From         string       `json:"from"`
	To           string       `json:"to"`
	Goal         int          `json:"goal"`
	TotalSteps   int          `json:"total_steps"`
	AverageSteps int          `json:"average_steps"`
	Days         []historyDay `json:"days"`
}

// buildHistory fills in the days with no steps, so a chart never has holes.
func buildHistory(totals []stats.Day, goal int, from, to time.Time) historyResponse {
	byDate := map[string]int{}
	for _, d := range totals {
		byDate[d.Date.Format(dateLayout)] = d.Steps
	}

	resp := historyResponse{From: from.Format(dateLayout), To: to.Format(dateLayout), Goal: goal, Days: []historyDay{}}
	for d := from; !d.After(to); d = d.AddDate(0, 0, 1) {
		steps := byDate[d.Format(dateLayout)]
		resp.TotalSteps += steps
		resp.Days = append(resp.Days, historyDay{
			Date:       d.Format(dateLayout),
			Steps:      steps,
			GoalMet:    steps >= goal,
			DistanceKm: stats.DistanceKm(steps),
			Calories:   stats.Calories(steps),
		})
	}
	resp.AverageSteps = resp.TotalSteps / len(resp.Days)
	return resp
}

type thisWeek struct {
	From        string `json:"from"`
	To          string `json:"to"`
	Steps       int    `json:"steps"`
	DaysGoalMet int    `json:"days_goal_met"`
}

type countryRank struct {
	Country    string `json:"country"`
	From       string `json:"from"`
	To         string `json:"to"`
	Rank       int    `json:"rank"`
	TotalUsers int    `json:"total_users"`
	Top100     bool   `json:"top_100"`
}

func (h *handler) stats(c *gin.Context) {
	today, ok := queryDay(c, "date")
	if !ok {
		return
	}
	ctx := c.Request.Context()
	settings, err := h.store.Settings(ctx, userID(c))
	if err != nil {
		serverError(c, err)
		return
	}
	days, err := h.store.DailyTotals(ctx, userID(c))
	if err != nil {
		serverError(c, err)
		return
	}

	current, longest := stats.Streaks(days, settings.DailyGoal, today)
	bestDay, bestWeek := stats.Records(days, today)
	from, to, weekSteps, weekGoals := stats.Week(days, settings.DailyGoal, today)

	lifetime, tracked := 0, 0
	for _, d := range days {
		if d.Date.After(today) {
			continue
		}
		lifetime += d.Steps
		if d.Steps > 0 {
			tracked++
		}
	}

	var rank *countryRank
	if settings.Country != nil {
		position, total, err := h.store.CountryRank(ctx, userID(c), *settings.Country, from, to)
		if err != nil {
			serverError(c, err)
			return
		}
		rank = &countryRank{
			Country:    *settings.Country,
			From:       from.Format(dateLayout),
			To:         to.Format(dateLayout),
			Rank:       position,
			TotalUsers: total,
			Top100:     position <= 100,
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"current_streak":       current,
		"longest_streak":       longest,
		"best_day":             bestDay,
		"best_week":            bestWeek,
		"lifetime_steps":       lifetime,
		"lifetime_distance_km": stats.DistanceKm(lifetime),
		"days_tracked":         tracked,
		"this_week": thisWeek{
			From:        from.Format(dateLayout),
			To:          to.Format(dateLayout),
			Steps:       weekSteps,
			DaysGoalMet: weekGoals,
		},
		"achievements": stats.Achievements(days, settings.DailyGoal, today),
		"country_rank": rank,
	})
}

// ---------- settings ----------

func (h *handler) getSettings(c *gin.Context) {
	settings, err := h.store.Settings(c.Request.Context(), userID(c))
	if err != nil {
		serverError(c, err)
		return
	}
	c.JSON(http.StatusOK, settings)
}

type settingsInput struct {
	DailyGoal int     `json:"daily_goal"`
	Country   *string `json:"country"`
}

func (h *handler) putSettings(c *gin.Context) {
	var in settingsInput
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, http.StatusBadRequest, "invalid_json", "request body must be valid JSON", nil)
		return
	}
	country, problems := validateSettings(in)
	if len(problems) > 0 {
		invalid(c, problems)
		return
	}
	if err := h.store.SaveSettings(c.Request.Context(), userID(c), in.DailyGoal, country); err != nil {
		serverError(c, err)
		return
	}
	c.JSON(http.StatusOK, store.Settings{DailyGoal: in.DailyGoal, Country: country})
}

// validateSettings also normalizes the country: "in" becomes "IN", "" becomes none.
func validateSettings(in settingsInput) (*string, map[string]string) {
	problems := map[string]string{}
	if in.DailyGoal < 1000 || in.DailyGoal > 100000 {
		problems["daily_goal"] = "must be between 1000 and 100000"
	}
	var country *string
	if in.Country != nil {
		code := strings.ToUpper(strings.TrimSpace(*in.Country))
		if code != "" {
			if !countryCode.MatchString(code) {
				problems["country"] = `must be a two-letter code such as "IN"`
			}
			country = &code
		}
	}
	return country, problems
}

// ---------- for other services (not reachable through the gateway) ----------

type totalsInput struct {
	UserIDs      []string `json:"user_ids"`
	From         string   `json:"from"`
	To           string   `json:"to"`
	RevealBefore string   `json:"reveal_before"` // optional: split off counts from this date on
}

func (h *handler) totals(c *gin.Context) {
	var in totalsInput
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, http.StatusBadRequest, "invalid_json", "request body must be valid JSON", nil)
		return
	}
	from, to, revealBefore, problems := validateTotals(in)
	if len(problems) > 0 {
		invalid(c, problems)
		return
	}
	totals, err := h.store.Totals(c.Request.Context(), in.UserIDs, from, to, revealBefore)
	if err != nil {
		serverError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"totals": totals})
}

func validateTotals(in totalsInput) (from, to, revealBefore time.Time, problems map[string]string) {
	problems = map[string]string{}
	if len(in.UserIDs) > maxTotalsUsers {
		problems["user_ids"] = fmt.Sprintf("at most %d ids", maxTotalsUsers)
	}
	for _, id := range in.UserIDs {
		if _, err := uuid.Parse(id); err != nil {
			problems["user_ids"] = "must all be uuids"
			break
		}
	}
	var err error
	if from, err = time.Parse(dateLayout, in.From); err != nil {
		problems["from"] = "must look like 2026-11-07"
	}
	if to, err = time.Parse(dateLayout, in.To); err != nil {
		problems["to"] = "must look like 2026-11-07"
	}
	revealBefore = to.AddDate(0, 0, 1) // by default everything is revealed
	if in.RevealBefore != "" {
		if revealBefore, err = time.Parse(dateLayout, in.RevealBefore); err != nil {
			problems["reveal_before"] = "must look like 2026-11-07"
		}
	}
	return from, to, revealBefore, problems
}

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

// ---------- dates ----------

// latestLocalDay is the newest date that is "today" anywhere on Earth right now
// (the world's clocks run up to 14 hours ahead of UTC).
func latestLocalDay(now time.Time) time.Time {
	t := now.UTC().Add(14 * time.Hour)
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// checkDay parses a date a client wants to save steps against. It must not be in
// the future for anyone on Earth, nor further back than maxDaysBack.
func checkDay(s string, maxDaysBack int, now time.Time) (time.Time, error) {
	day, err := time.Parse(dateLayout, s)
	if err != nil {
		return day, fmt.Errorf("must look like 2026-11-07")
	}
	latest := latestLocalDay(now)
	if day.After(latest) {
		return day, fmt.Errorf("must not be in the future")
	}
	if day.Before(latest.AddDate(0, 0, -(maxDaysBack + 1))) {
		return day, fmt.Errorf("must be within the last %d days", maxDaysBack)
	}
	return day, nil
}

// queryDay reads a date from the query string and answers 422 itself if it is
// malformed. An empty value means today (UTC).
func queryDay(c *gin.Context, key string) (time.Time, bool) {
	value := c.Query(key)
	if value == "" {
		now := time.Now().UTC()
		return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC), true
	}
	day, err := time.Parse(dateLayout, value)
	if err != nil {
		invalid(c, map[string]string{key: "must look like 2026-11-07"})
		return time.Time{}, false
	}
	return day, true
}
