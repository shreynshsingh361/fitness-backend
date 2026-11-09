package stats

import (
	"testing"
	"time"
)

func date(s string) time.Time {
	d, err := time.Parse(dateLayout, s)
	if err != nil {
		panic(err)
	}
	return d
}

// run makes consecutive days starting at `start`, one per entry in steps.
func run(start string, steps ...int) []Day {
	d := date(start)
	days := make([]Day, 0, len(steps))
	for _, s := range steps {
		days = append(days, Day{Date: d, Steps: s})
		d = d.AddDate(0, 0, 1)
	}
	return days
}

func find(t *testing.T, list []Achievement, id string) Achievement {
	t.Helper()
	for _, a := range list {
		if a.ID == id {
			return a
		}
	}
	t.Fatalf("no achievement %q", id)
	return Achievement{}
}

func TestEstimates(t *testing.T) {
	if got := DistanceKm(10000); got != 7.62 {
		t.Errorf("DistanceKm(10000) = %v, want 7.62", got)
	}
	if got := Calories(10000); got != 400 {
		t.Errorf("Calories(10000) = %v, want 400", got)
	}
	if DistanceKm(0) != 0 || Calories(0) != 0 {
		t.Error("zero steps must give zero distance and calories")
	}
}

func TestWeekStartIsMonday(t *testing.T) {
	// 2026-11-07 is a Saturday.
	cases := map[string]string{
		"2026-11-02": "2026-11-02", // Monday stays
		"2026-11-07": "2026-11-02", // Saturday
		"2026-11-08": "2026-11-02", // Sunday belongs to the week that started Monday
		"2026-11-09": "2026-11-09", // next Monday
		"2027-01-01": "2026-12-28", // across a year end
	}
	for in, want := range cases {
		if got := WeekStart(date(in)).Format(dateLayout); got != want {
			t.Errorf("WeekStart(%s) = %s, want %s", in, got, want)
		}
	}
}

func TestStreaks(t *testing.T) {
	const goal = 1000
	today := date("2026-11-07")

	cases := []struct {
		name             string
		days             []Day
		current, longest int
	}{
		{"no data", nil, 0, 0},
		{"today only", run("2026-11-07", 1000), 1, 1},
		{"three days ending today", run("2026-11-05", 1000, 1000, 1000), 3, 3},
		{
			// Today is not over yet, so missing the goal so far must not end the streak.
			"unfinished today keeps the streak alive",
			run("2026-11-04", 1000, 1000, 1000, 10), 3, 3,
		},
		{
			"a whole missed day ends it",
			run("2026-11-03", 1000, 1000, 10, 1000, 10), 1, 2,
		},
		{
			// A day with no row at all (a gap) breaks a run just like a low day.
			"gap in the data breaks the run",
			append(run("2026-11-01", 1000, 1000), run("2026-11-04", 1000)...), 0, 2,
		},
		{
			"streak that ended long ago is only the longest",
			run("2026-10-01", 1000, 1000, 1000, 1000), 0, 4,
		},
		{"a date after today is ignored", run("2026-11-08", 1000), 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			current, longest := Streaks(tc.days, goal, today)
			if current != tc.current || longest != tc.longest {
				t.Errorf("got current=%d longest=%d, want current=%d longest=%d", current, longest, tc.current, tc.longest)
			}
		})
	}
}

func TestRecords(t *testing.T) {
	today := date("2026-11-07")

	if d, w := Records(nil, today); d != nil || w != nil {
		t.Fatal("no steps must give no records")
	}
	if d, w := Records(run("2026-11-05", 0, 0), today); d != nil || w != nil {
		t.Fatal("days of zero steps must give no records")
	}

	// Week of Oct 26: 3000 + 4000 = 7000. Week of Nov 2: 5000 + 1000 = 6000.
	days := append(run("2026-10-26", 3000, 4000), run("2026-11-02", 5000, 1000)...)
	bestDay, bestWeek := Records(days, today)
	if bestDay.Date != "2026-11-02" || bestDay.Steps != 5000 {
		t.Errorf("best day = %+v", bestDay)
	}
	if bestWeek.From != "2026-10-26" || bestWeek.To != "2026-11-01" || bestWeek.Steps != 7000 {
		t.Errorf("best week = %+v", bestWeek)
	}
}

func TestRecordsTiedWeeksPickTheEarlierOne(t *testing.T) {
	days := append(run("2026-10-19", 500), run("2026-10-26", 500)...)
	days = append(days, run("2026-11-02", 500)...)
	for i := 0; i < 20; i++ { // map order is random, so try repeatedly
		_, week := Records(days, date("2026-11-07"))
		if week.From != "2026-10-19" {
			t.Fatalf("tied weeks gave %s, want the earliest", week.From)
		}
	}
}

func TestWeekTotals(t *testing.T) {
	days := append(run("2026-11-01", 9999), run("2026-11-02", 1000, 500, 2000)...) // Nov 1 is last week
	from, to, steps, met := Week(days, 1000, date("2026-11-04"))
	if from.Format(dateLayout) != "2026-11-02" || to.Format(dateLayout) != "2026-11-08" {
		t.Errorf("range = %s..%s", from.Format(dateLayout), to.Format(dateLayout))
	}
	if steps != 3500 || met != 2 {
		t.Errorf("steps=%d met=%d, want 3500 and 2", steps, met)
	}
}

func TestAchievementsStartUnearned(t *testing.T) {
	for _, a := range Achievements(nil, 10000, date("2026-11-07")) {
		if a.Earned || a.EarnedOn != nil {
			t.Errorf("%s earned with no steps", a.ID)
		}
		if a.Title == "" || a.Description == "" || a.Period == "" {
			t.Errorf("%s is missing text: %+v", a.ID, a)
		}
	}
}

func TestDailyAchievementsOnlyLookAtToday(t *testing.T) {
	const goal = 1000
	days := run("2026-11-06", 5000, 500) // huge yesterday, low today
	list := Achievements(days, goal, date("2026-11-07"))
	if find(t, list, "goal_today").Earned || find(t, list, "double_today").Earned {
		t.Error("yesterday's steps must not earn today's achievements")
	}

	list = Achievements(run("2026-11-07", 2000), goal, date("2026-11-07"))
	if !find(t, list, "goal_today").Earned || !find(t, list, "double_today").Earned {
		t.Error("2x goal today must earn both daily achievements")
	}
}

func TestWeeklyAchievements(t *testing.T) {
	const goal = 1000
	// Saturday: Monday to Friday all at goal, in the current week.
	days := run("2026-11-02", 1000, 1000, 1000, 1000, 1000)
	list := Achievements(days, goal, date("2026-11-07"))
	five := find(t, list, "week_5_goals")
	if !five.Earned || *five.EarnedOn != "2026-11-06" {
		t.Errorf("week_5_goals = %+v, want earned on the fifth day", five)
	}
	if find(t, list, "week_70k").Earned {
		t.Error("5,000 steps must not earn week_70k")
	}

	// The same effort last week counts for nothing this week.
	list = Achievements(run("2026-10-26", 1000, 1000, 1000, 1000, 1000), goal, date("2026-11-07"))
	if find(t, list, "week_5_goals").Earned {
		t.Error("last week must not earn this week's achievement")
	}
}

func TestAllTimeAchievementsKeepTheirFirstDate(t *testing.T) {
	const goal = 1000
	days := run("2026-10-01", 1000, 1000, 1000, 10, 1000, 1000, 1000, 1000, 1000, 1000, 1000, 20000, 100000)
	list := Achievements(days, goal, date("2026-11-07"))

	want := map[string]string{
		"first_steps":   "2026-10-01",
		"first_goal":    "2026-10-01",
		"streak_3":      "2026-10-03",
		"streak_7":      "2026-10-11",
		"day_20k":       "2026-10-12",
		"lifetime_100k": "2026-10-13",
		"marathon":      "2026-10-13",
	}
	for id, on := range want {
		a := find(t, list, id)
		if !a.Earned || *a.EarnedOn != on {
			t.Errorf("%s: earned=%v on=%v, want %s", id, a.Earned, a.EarnedOn, on)
		}
	}
	if find(t, list, "streak_30").Earned || find(t, list, "lifetime_1m").Earned {
		t.Error("streak_30 and lifetime_1m must not be earned yet")
	}
}

func TestGapBreaksAchievementStreak(t *testing.T) {
	const goal = 1000
	days := append(run("2026-10-01", 1000, 1000), run("2026-10-04", 1000, 1000)...)
	if find(t, Achievements(days, goal, date("2026-11-07")), "streak_3").Earned {
		t.Error("four goal days with a gap are not 3 in a row")
	}
}

func TestFutureDatesAreIgnored(t *testing.T) {
	list := Achievements(run("2026-11-08", 5000), 1000, date("2026-11-07"))
	if find(t, list, "first_steps").Earned {
		t.Error("a date after today must not earn anything")
	}
}
