// Package leaderboard holds the rules of a leaderboard: which dates a period
// covers, who ranks where, and what mystery mode hides. There is no database and
// no HTTP here, only plain functions, which makes the rules easy to read and test.
package leaderboard

import (
	"errors"
	"sort"
	"time"
)

const (
	Day       = "day"
	Yesterday = "yesterday"
	Week      = "week"
	Month     = "month"
	Challenge = "challenge"
)

var (
	ErrUnknownPeriod = errors.New("unknown period")
	ErrNoChallenge   = errors.New("this group has no fixed-date challenge")
)

// Range turns a period and a date into the dates it covers. A week runs Monday to
// Sunday; a month is the calendar month containing the date.
func Range(period string, date time.Time, challengeStart, challengeEnd *time.Time) (from, to time.Time, err error) {
	switch period {
	case Day:
		return date, date, nil
	case Yesterday: // the day before the date the person sent, which is their own today
		d := date.AddDate(0, 0, -1)
		return d, d, nil
	case Week:
		from = date.AddDate(0, 0, -((int(date.Weekday()) + 6) % 7))
		return from, from.AddDate(0, 0, 6), nil
	case Month:
		from = time.Date(date.Year(), date.Month(), 1, 0, 0, 0, 0, time.UTC)
		return from, from.AddDate(0, 1, -1), nil
	case Challenge:
		if challengeStart == nil || challengeEnd == nil {
			return from, to, ErrNoChallenge
		}
		return *challengeStart, *challengeEnd, nil
	}
	return from, to, ErrUnknownPeriod
}

// RevealCutoff is the first date mystery mode still hides at this moment.
//
// Everyone's steps are saved against their own local date, so a date is not over
// until it has ended everywhere. The last place on Earth to finish a date is 12
// hours behind UTC. Revealing any sooner would show the counts of people in other
// time zones who are still walking that same date.
func RevealCutoff(now time.Time) time.Time {
	t := now.UTC().Add(-12 * time.Hour)
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// Row is one person's place on a board.
type Row struct {
	Rank   int
	UserID string
	Steps  int
}

// Rank orders people by steps, best first. Equal steps share a rank, and the next
// rank skips ahead (1, 2, 2, 4). People missing from `steps` count as zero. Ties are
// listed by id, so the order does not change between requests.
func Rank(userIDs []string, steps map[string]int) []Row {
	rows := make([]Row, 0, len(userIDs))
	for _, id := range userIDs {
		rows = append(rows, Row{UserID: id, Steps: steps[id]})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Steps != rows[j].Steps {
			return rows[i].Steps > rows[j].Steps
		}
		return rows[i].UserID < rows[j].UserID
	})
	for i := range rows {
		if i > 0 && rows[i].Steps == rows[i-1].Steps {
			rows[i].Rank = rows[i-1].Rank
		} else {
			rows[i].Rank = i + 1
		}
	}
	return rows
}

// Visible decides what a viewer may see of one person's count. You always see your
// own. In mystery mode everyone else's count covers only the dates already revealed,
// or nothing at all if the whole period is still hidden.
func Visible(isViewer bool, total, revealed int, mysteryActive, wholePeriodHidden bool) (steps *int, hidden bool) {
	switch {
	case isViewer || !mysteryActive:
		return &total, false
	case wholePeriodHidden:
		return nil, true
	default:
		return &revealed, true
	}
}
