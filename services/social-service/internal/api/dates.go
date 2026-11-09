package api

import (
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"fitness/social-service/internal/store"
)

const (
	dateLayout = "2006-01-02"

	// A fixed-date challenge runs from 1 day to 1 year, and a report can span up to a year.
	maxRangeDays = 366
)

func today() time.Time {
	now := time.Now().UTC()
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
}

func intQuery(c *gin.Context, key string, fallback int) int {
	if v, err := strconv.Atoi(c.Query(key)); err == nil {
		return v
	}
	return fallback
}

// queryDay reads ?date= and answers 422 itself if it is malformed. No value means
// today (UTC). Clients send their own local date, because the server cannot know it.
func queryDay(c *gin.Context) (time.Time, bool) {
	value := c.Query("date")
	if value == "" {
		return today(), true
	}
	day, err := time.Parse(dateLayout, value)
	if err != nil {
		invalid(c, map[string]string{"date": "must look like 2026-11-07"})
		return time.Time{}, false
	}
	return day, true
}

// daysBetween counts both ends: Monday to Monday is 1 day.
func daysBetween(from, to time.Time) int {
	return int(to.Sub(from).Hours()/24) + 1
}

// challengeDates turns a group's stored challenge into dates, if it has one.
func challengeDates(g store.Group) (start, end *time.Time) {
	if g.ChallengeStart == nil || g.ChallengeEnd == nil {
		return nil, nil
	}
	s, err1 := time.Parse(dateLayout, *g.ChallengeStart)
	e, err2 := time.Parse(dateLayout, *g.ChallengeEnd)
	if err1 != nil || err2 != nil {
		return nil, nil
	}
	return &s, &e
}

// validateChallenge checks the dates an admin picked for a fixed-date challenge.
func validateChallenge(start, end string) (from, to time.Time, problems map[string]string) {
	problems = map[string]string{}
	from, err := time.Parse(dateLayout, start)
	if err != nil {
		problems["start"] = "must look like 2026-11-07"
	}
	to, err = time.Parse(dateLayout, end)
	if err != nil {
		problems["end"] = "must look like 2026-11-07"
	}
	if len(problems) == 0 {
		if to.Before(from) {
			problems["end"] = "must not be before the start date"
		} else if daysBetween(from, to) > maxRangeDays {
			problems["end"] = "a challenge can last at most 1 year"
		}
	}
	return from, to, problems
}
