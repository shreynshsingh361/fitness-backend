package api

import (
	"encoding/csv"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"fitness/social-service/internal/clients"
	"fitness/social-service/internal/leaderboard"
)

// timeNow is a variable so a test could freeze the clock.
var timeNow = time.Now

type reportRow struct {
	Rank        int
	Name        string
	Steps       int
	Average     int
	DaysActive  int
	BestDay     int
	DaysGoalMet int
}

// groupReport lets an admin download everyone's totals for any date range.
func (h *handler) groupReport(c *gin.Context) {
	group, ok := h.admin(c)
	if !ok {
		return
	}
	from, to, ok := reportRange(c, group.ChallengeStart, group.ChallengeEnd)
	if !ok {
		return
	}

	// In mystery mode the report stops before the first date still hidden, so an
	// admin cannot simply download what the leaderboard is holding back.
	empty := false
	if group.MysteryMode {
		cutoff := leaderboard.RevealCutoff(timeNow())
		if !to.Before(cutoff) {
			to = cutoff.AddDate(0, 0, -1)
		}
		empty = from.After(to)
	}

	people, err := h.visibleMembers(c, group.ID)
	if err != nil {
		serverError(c, err)
		return
	}

	totals := map[string]clients.UserTotal{}
	if !empty {
		totals, err = h.steps.Totals(requestContext(c), clients.TotalsRequest{
			UserIDs: people,
			From:    from.Format(dateLayout),
			To:      to.Format(dateLayout),
		})
		if err != nil {
			log.Printf("req=%s step totals unavailable: %v", c.GetHeader("X-Request-ID"), err)
			fail(c, http.StatusServiceUnavailable, "steps_unavailable", "step data is temporarily unavailable", nil)
			return
		}
	}
	// A report is no use with every name missing, so here that is an error too.
	names, err := h.names.Names(requestContext(c), people)
	if err != nil {
		log.Printf("req=%s names unavailable: %v", c.GetHeader("X-Request-ID"), err)
		fail(c, http.StatusServiceUnavailable, "names_unavailable", "member names are temporarily unavailable", nil)
		return
	}

	steps := make(map[string]int, len(totals))
	for id, total := range totals {
		steps[id] = total.Steps
	}
	days := 1
	if !empty {
		days = daysBetween(from, to)
	}
	rows := make([]reportRow, 0, len(people))
	for _, row := range leaderboard.Rank(people, steps) {
		total := totals[row.UserID]
		rows = append(rows, reportRow{
			Rank:        row.Rank,
			Name:        nameOf(names, row.UserID),
			Steps:       row.Steps,
			Average:     row.Steps / days,
			DaysActive:  total.DaysActive,
			BestDay:     total.BestDay,
			DaysGoalMet: total.DaysGoalMet,
		})
	}

	filename := fmt.Sprintf("steps-report-%s-to-%s.csv", from.Format(dateLayout), to.Format(dateLayout))
	c.Header("Content-Type", "text/csv; charset=utf-8")
	c.Header("Content-Disposition", `attachment; filename="`+filename+`"`)
	c.Status(http.StatusOK)
	if err := writeReport(c.Writer, rows); err != nil {
		log.Printf("req=%s writing report: %v", c.GetHeader("X-Request-ID"), err)
	}
}

// reportRange reads ?from=&to=. It defaults to the challenge dates if the group has
// them, otherwise the current month.
func reportRange(c *gin.Context, challengeStart, challengeEnd *string) (from, to time.Time, ok bool) {
	defaultFrom, defaultTo, _ := leaderboard.Range(leaderboard.Month, today(), nil, nil)
	if challengeStart != nil && challengeEnd != nil {
		if s, err := time.Parse(dateLayout, *challengeStart); err == nil {
			defaultFrom = s
		}
		if e, err := time.Parse(dateLayout, *challengeEnd); err == nil {
			defaultTo = e
		}
	}

	parse := func(key string, fallback time.Time) (time.Time, bool) {
		value := c.Query(key)
		if value == "" {
			return fallback, true
		}
		day, err := time.Parse(dateLayout, value)
		if err != nil {
			invalid(c, map[string]string{key: "must look like 2026-11-07"})
			return time.Time{}, false
		}
		return day, true
	}
	if from, ok = parse("from", defaultFrom); !ok {
		return from, to, false
	}
	if to, ok = parse("to", defaultTo); !ok {
		return from, to, false
	}
	if to.Before(from) {
		invalid(c, map[string]string{"to": "must not be before from"})
		return from, to, false
	}
	if daysBetween(from, to) > maxRangeDays {
		invalid(c, map[string]string{"to": "a report can cover at most 1 year"})
		return from, to, false
	}
	return from, to, true
}

func writeReport(w io.Writer, rows []reportRow) error {
	out := csv.NewWriter(w)
	if err := out.Write([]string{"rank", "name", "total_steps", "average_steps_per_day", "days_active", "best_day", "days_goal_met"}); err != nil {
		return err
	}
	for _, r := range rows {
		err := out.Write([]string{
			strconv.Itoa(r.Rank),
			csvSafe(r.Name),
			strconv.Itoa(r.Steps),
			strconv.Itoa(r.Average),
			strconv.Itoa(r.DaysActive),
			strconv.Itoa(r.BestDay),
			strconv.Itoa(r.DaysGoalMet),
		})
		if err != nil {
			return err
		}
	}
	out.Flush()
	return out.Error()
}

// csvSafe stops a formula in someone's name from running when an admin opens the
// report in a spreadsheet. A name like =HYPERLINK(...) is otherwise treated as a
// formula. A leading apostrophe makes the spreadsheet treat it as plain text.
func csvSafe(value string) string {
	if value != "" && strings.ContainsRune("=+-@\t\r", rune(value[0])) {
		return "'" + value
	}
	return value
}
