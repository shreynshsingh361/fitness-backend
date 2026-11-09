// Package stats holds the step maths: distance and calorie estimates, streaks,
// records and achievements. There is no database and no HTTP here, only plain
// functions on a list of days, which makes them easy to read and to test.
package stats

import (
	"math"
	"time"
)

const (
	dateLayout = "2006-01-02"

	// Rough averages for an adult walking. Nothing here knows the person's
	// height or weight, so the API calls these numbers estimates.
	strideMeters    = 0.762
	caloriesPerStep = 0.04
)

// Day is one calendar day's total. Dates are at midnight UTC and lists of days
// are sorted oldest first, with no duplicates and possibly with gaps.
type Day struct {
	Date  time.Time
	Steps int
}

func DistanceKm(steps int) float64 {
	return math.Round(float64(steps)*strideMeters/10) / 100
}

func Calories(steps int) int {
	return int(math.Round(float64(steps) * caloriesPerStep))
}

// WeekStart returns the Monday of the week containing d.
func WeekStart(d time.Time) time.Time {
	return d.AddDate(0, 0, -((int(d.Weekday()) + 6) % 7))
}

// Streaks counts days in a row at or above the goal. Today not being finished
// does not break the current streak: it only ends once a whole day is missed.
func Streaks(days []Day, goal int, today time.Time) (current, longest int) {
	met := map[string]bool{}
	var previous time.Time
	run := 0
	for _, d := range days {
		if d.Date.After(today) || d.Steps < goal {
			run = 0
			continue
		}
		met[d.Date.Format(dateLayout)] = true
		if run > 0 && d.Date.Equal(previous.AddDate(0, 0, 1)) {
			run++
		} else {
			run = 1
		}
		previous = d.Date
		if run > longest {
			longest = run
		}
	}

	cursor := today
	if !met[cursor.Format(dateLayout)] {
		cursor = cursor.AddDate(0, 0, -1)
	}
	for met[cursor.Format(dateLayout)] {
		current++
		cursor = cursor.AddDate(0, 0, -1)
	}
	return current, longest
}

type BestDay struct {
	Date  string `json:"date"`
	Steps int    `json:"steps"`
}

type BestWeek struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Steps int    `json:"steps"`
}

// Records finds the highest day and the highest Monday-to-Sunday week.
// Both are nil until the user has walked at all.
func Records(days []Day, today time.Time) (*BestDay, *BestWeek) {
	var bestDay *BestDay
	weeks := map[time.Time]int{}
	for _, d := range days {
		if d.Date.After(today) || d.Steps == 0 {
			continue
		}
		if bestDay == nil || d.Steps > bestDay.Steps {
			bestDay = &BestDay{Date: d.Date.Format(dateLayout), Steps: d.Steps}
		}
		weeks[WeekStart(d.Date)] += d.Steps
	}

	var bestWeek *BestWeek
	var bestStart time.Time
	for start, steps := range weeks {
		// Equal weeks: the earlier one holds the record (map order is random).
		if bestWeek == nil || steps > bestWeek.Steps || (steps == bestWeek.Steps && start.Before(bestStart)) {
			bestStart = start
			bestWeek = &BestWeek{
				From:  start.Format(dateLayout),
				To:    start.AddDate(0, 0, 6).Format(dateLayout),
				Steps: steps,
			}
		}
	}
	return bestDay, bestWeek
}

// Week adds up one Monday-to-Sunday week.
func Week(days []Day, goal int, today time.Time) (from, to time.Time, steps, daysGoalMet int) {
	from = WeekStart(today)
	to = from.AddDate(0, 0, 6)
	for _, d := range days {
		if d.Date.Before(from) || d.Date.After(to) {
			continue
		}
		steps += d.Steps
		if d.Steps >= goal {
			daysGoalMet++
		}
	}
	return from, to, steps, daysGoalMet
}

// Achievement is worked out from the step history whenever it is asked for, so
// there is nothing to store and it can never disagree with the steps themselves.
type Achievement struct {
	ID          string  `json:"id"`
	Title       string  `json:"title"`
	Description string  `json:"description"`
	Period      string  `json:"period"` // daily, weekly or all_time
	Earned      bool    `json:"earned"`
	EarnedOn    *string `json:"earned_on"`
}

// Achievements evaluates every achievement. Daily and weekly ones only look at
// the day and week containing today; the rest are earned for good.
func Achievements(days []Day, goal int, today time.Time) []Achievement {
	list := []Achievement{
		{ID: "goal_today", Title: "Goal reached", Description: "Reach your step goal today", Period: "daily"},
		{ID: "double_today", Title: "Double time", Description: "Walk twice your goal today", Period: "daily"},
		{ID: "week_5_goals", Title: "Consistent week", Description: "Reach your goal on 5 days this week", Period: "weekly"},
		{ID: "week_70k", Title: "70K week", Description: "Walk 70,000 steps this week", Period: "weekly"},
		{ID: "first_steps", Title: "First steps", Description: "Log your first steps", Period: "all_time"},
		{ID: "first_goal", Title: "On target", Description: "Reach your goal for the first time", Period: "all_time"},
		{ID: "day_20k", Title: "Big day", Description: "Walk 20,000 steps in one day", Period: "all_time"},
		{ID: "streak_3", Title: "3-day streak", Description: "Reach your goal 3 days in a row", Period: "all_time"},
		{ID: "streak_7", Title: "7-day streak", Description: "Reach your goal 7 days in a row", Period: "all_time"},
		{ID: "streak_30", Title: "30-day streak", Description: "Reach your goal 30 days in a row", Period: "all_time"},
		{ID: "marathon", Title: "Marathon legs", Description: "Walk 42.2 km in total", Period: "all_time"},
		{ID: "lifetime_100k", Title: "100K club", Description: "Walk 100,000 steps in total", Period: "all_time"},
		{ID: "lifetime_1m", Title: "A million steps", Description: "Walk 1,000,000 steps in total", Period: "all_time"},
	}

	earned := map[string]string{} // achievement id -> the date it was first earned
	earn := func(id string, on time.Time) {
		if _, done := earned[id]; !done {
			earned[id] = on.Format(dateLayout)
		}
	}

	weekStart := WeekStart(today)
	var cumulative, weekSteps, weekGoals, run int
	var previous time.Time
	for _, d := range days { // oldest first, so the first time wins
		if d.Date.After(today) {
			continue
		}
		cumulative += d.Steps
		if d.Steps > 0 {
			earn("first_steps", d.Date)
		}
		if d.Steps >= 20000 {
			earn("day_20k", d.Date)
		}
		if cumulative >= 100000 {
			earn("lifetime_100k", d.Date)
		}
		if cumulative >= 1000000 {
			earn("lifetime_1m", d.Date)
		}
		if DistanceKm(cumulative) >= 42.2 {
			earn("marathon", d.Date)
		}

		if d.Steps >= goal {
			earn("first_goal", d.Date)
			if run > 0 && d.Date.Equal(previous.AddDate(0, 0, 1)) {
				run++
			} else {
				run = 1
			}
			previous = d.Date
			if run >= 3 {
				earn("streak_3", d.Date)
			}
			if run >= 7 {
				earn("streak_7", d.Date)
			}
			if run >= 30 {
				earn("streak_30", d.Date)
			}
		} else {
			run = 0
		}

		if d.Date.Equal(today) {
			if d.Steps >= goal {
				earn("goal_today", d.Date)
			}
			if d.Steps >= 2*goal {
				earn("double_today", d.Date)
			}
		}
		if !d.Date.Before(weekStart) {
			weekSteps += d.Steps
			if d.Steps >= goal {
				weekGoals++
			}
			if weekGoals >= 5 {
				earn("week_5_goals", d.Date)
			}
			if weekSteps >= 70000 {
				earn("week_70k", d.Date)
			}
		}
	}

	for i := range list {
		if on, ok := earned[list[i].ID]; ok {
			list[i].Earned = true
			list[i].EarnedOn = &on
		}
	}
	return list
}
