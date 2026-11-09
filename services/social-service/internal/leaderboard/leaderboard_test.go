package leaderboard

import (
	"errors"
	"testing"
	"time"
)

const layout = "2006-01-02"

func date(s string) time.Time {
	d, err := time.Parse(layout, s)
	if err != nil {
		panic(err)
	}
	return d
}

func TestRange(t *testing.T) {
	start, end := date("2026-10-15"), date("2026-11-15")
	cases := []struct {
		period, date, from, to string
	}{
		{Day, "2026-11-07", "2026-11-07", "2026-11-07"},
		{Yesterday, "2026-11-07", "2026-11-06", "2026-11-06"},
		{Yesterday, "2026-11-01", "2026-10-31", "2026-10-31"}, // back across a month end
		{Week, "2026-11-07", "2026-11-02", "2026-11-08"},      // Saturday
		{Week, "2026-11-02", "2026-11-02", "2026-11-08"},      // Monday
		{Week, "2026-11-08", "2026-11-02", "2026-11-08"},      // Sunday is the end of the week
		{Week, "2027-01-01", "2026-12-28", "2027-01-03"},      // across a year end
		{Month, "2026-11-07", "2026-11-01", "2026-11-30"},
		{Month, "2028-02-10", "2028-02-01", "2028-02-29"}, // a leap year
		{Month, "2026-12-31", "2026-12-01", "2026-12-31"}, // December does not spill over
		{Challenge, "2026-11-07", "2026-10-15", "2026-11-15"},
	}
	for _, tc := range cases {
		from, to, err := Range(tc.period, date(tc.date), &start, &end)
		if err != nil {
			t.Errorf("%s %s: %v", tc.period, tc.date, err)
			continue
		}
		if from.Format(layout) != tc.from || to.Format(layout) != tc.to {
			t.Errorf("%s %s = %s..%s, want %s..%s", tc.period, tc.date, from.Format(layout), to.Format(layout), tc.from, tc.to)
		}
	}
}

func TestRangeErrors(t *testing.T) {
	if _, _, err := Range(Challenge, date("2026-11-07"), nil, nil); !errors.Is(err, ErrNoChallenge) {
		t.Errorf("no challenge set: err = %v", err)
	}
	if _, _, err := Range("year", date("2026-11-07"), nil, nil); !errors.Is(err, ErrUnknownPeriod) {
		t.Errorf("unknown period: err = %v", err)
	}
}

func TestRevealCutoff(t *testing.T) {
	cases := []struct {
		now  string
		want string
		why  string
	}{
		{"2026-11-07T11:59:00Z", "2026-11-06", "at UTC-12 it is still Nov 6 (23:59)"},
		{"2026-11-07T12:00:00Z", "2026-11-07", "Nov 6 has now ended everywhere"},
		{"2026-11-07T23:00:00Z", "2026-11-07", "Nov 7 is still running in most places"},
		{"2026-11-08T03:30:00Z", "2026-11-07", "09:00 on Nov 8 in India, but Nov 7 is not over in the Americas"},
	}
	for _, tc := range cases {
		now, err := time.Parse(time.RFC3339, tc.now)
		if err != nil {
			t.Fatal(err)
		}
		if got := RevealCutoff(now).Format(layout); got != tc.want {
			t.Errorf("at %s: cutoff %s, want %s (%s)", tc.now, got, tc.want, tc.why)
		}
	}
}

func TestRevealCutoffIgnoresTheCallersTimeZone(t *testing.T) {
	india := time.FixedZone("IST", 5*3600+1800)
	now := time.Date(2026, 11, 8, 9, 0, 0, 0, india) // 03:30 UTC
	if got := RevealCutoff(now).Format(layout); got != "2026-11-07" {
		t.Errorf("cutoff = %s, want 2026-11-07", got)
	}
}

func TestRankOrderAndTies(t *testing.T) {
	rows := Rank(
		[]string{"a", "b", "c", "d", "e"},
		map[string]int{"a": 100, "b": 300, "c": 300, "d": 50}, // e has no steps at all
	)
	want := []Row{
		{Rank: 1, UserID: "b", Steps: 300},
		{Rank: 1, UserID: "c", Steps: 300},
		{Rank: 3, UserID: "a", Steps: 100},
		{Rank: 4, UserID: "d", Steps: 50},
		{Rank: 5, UserID: "e", Steps: 0},
	}
	if len(rows) != len(want) {
		t.Fatalf("got %d rows", len(rows))
	}
	for i := range want {
		if rows[i] != want[i] {
			t.Errorf("row %d = %+v, want %+v", i, rows[i], want[i])
		}
	}
}

func TestRankIsStableAcrossInputOrder(t *testing.T) {
	steps := map[string]int{"a": 5, "b": 5, "c": 5}
	first := Rank([]string{"a", "b", "c"}, steps)
	second := Rank([]string{"c", "a", "b"}, steps)
	for i := range first {
		if first[i] != second[i] {
			t.Fatalf("order depends on the input: %+v vs %+v", first, second)
		}
	}
}

func TestRankEmpty(t *testing.T) {
	if rows := Rank(nil, nil); len(rows) != 0 {
		t.Fatalf("got %+v", rows)
	}
}

func TestVisible(t *testing.T) {
	cases := []struct {
		name                     string
		isViewer, mystery, whole bool
		wantSteps                int // ignored when wantNil
		wantNil, wantHidden      bool
	}{
		{name: "no mystery: everyone is visible", wantSteps: 900},
		{name: "mystery: I always see my own", isViewer: true, mystery: true, whole: true, wantSteps: 900},
		{name: "mystery, part of the period hidden: only revealed steps", mystery: true, wantSteps: 400, wantHidden: true},
		{name: "mystery, whole period hidden: nothing", mystery: true, whole: true, wantNil: true, wantHidden: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			steps, hidden := Visible(tc.isViewer, 900, 400, tc.mystery, tc.whole)
			if hidden != tc.wantHidden {
				t.Errorf("hidden = %v, want %v", hidden, tc.wantHidden)
			}
			if tc.wantNil {
				if steps != nil {
					t.Errorf("steps = %d, want none", *steps)
				}
				return
			}
			if steps == nil || *steps != tc.wantSteps {
				t.Errorf("steps = %v, want %d", steps, tc.wantSteps)
			}
		})
	}
}
