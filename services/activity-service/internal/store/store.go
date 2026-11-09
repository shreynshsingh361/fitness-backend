// Package store holds every SQL query the activity service runs.
package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"fitness/activity-service/internal/stats"
)

const DefaultDailyGoal = 10000

type Store struct {
	db *pgxpool.Pool
}

// Ping reports whether the database answers. The readiness check uses it.
func (s *Store) Ping(ctx context.Context) error { return s.db.Ping(ctx) }

func New(db *pgxpool.Pool) *Store { return &Store{db: db} }

// DayCount is one day's count from one source.
type DayCount struct {
	Day   time.Time
	Steps int
}

type SourceSteps struct {
	Source string `json:"source"`
	Steps  int    `json:"steps"`
}

type Settings struct {
	DailyGoal int     `json:"daily_goal"`
	Country   *string `json:"country"`
	IsDefault bool    `json:"is_default"`
}

// UserTotal is one person's steps over a date range, for leaderboards and reports.
type UserTotal struct {
	UserID        string `json:"user_id"`
	Steps         int    `json:"steps"`
	RevealedSteps int    `json:"revealed_steps"` // only the dates before reveal_before
	DaysActive    int    `json:"days_active"`
	BestDay       int    `json:"best_day"`
	DaysGoalMet   int    `json:"days_goal_met"`
}

// Sync saves a device's counts. A device's count for a day only ever goes up, so
// keeping the higher number makes a retried or out-of-order sync harmless.
func (s *Store) Sync(ctx context.Context, userID, source string, days []DayCount) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	for _, d := range days {
		_, err := tx.Exec(ctx,
			`INSERT INTO step_entries (user_id, day, source, steps) VALUES ($1, $2, $3, $4)
			 ON CONFLICT (user_id, day, source)
			 DO UPDATE SET steps = GREATEST(step_entries.steps, EXCLUDED.steps), updated_at = now()`,
			userID, d.Day, source, d.Steps)
		if err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// Sources lists what each source counted on one day, highest first.
func (s *Store) Sources(ctx context.Context, userID string, day time.Time) ([]SourceSteps, error) {
	rows, err := s.db.Query(ctx,
		`SELECT source, steps FROM step_entries
		 WHERE user_id = $1 AND day = $2 ORDER BY steps DESC, source`, userID, day)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	sources := []SourceSteps{}
	for rows.Next() {
		var src SourceSteps
		if err := rows.Scan(&src.Source, &src.Steps); err != nil {
			return nil, err
		}
		sources = append(sources, src)
	}
	return sources, rows.Err()
}

// DailyTotals returns every day this user has steps for, oldest first. A day's
// total is the highest count of any source: never the sum, because a phone and a
// watch count the same walk.
func (s *Store) DailyTotals(ctx context.Context, userID string) ([]stats.Day, error) {
	rows, err := s.db.Query(ctx,
		`SELECT day, max(steps) FROM step_entries
		 WHERE user_id = $1 GROUP BY day ORDER BY day`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	days := []stats.Day{}
	for rows.Next() {
		var d stats.Day
		if err := rows.Scan(&d.Date, &d.Steps); err != nil {
			return nil, err
		}
		days = append(days, d)
	}
	return days, rows.Err()
}

// Settings returns the user's settings, or the defaults if they never saved any.
func (s *Store) Settings(ctx context.Context, userID string) (Settings, error) {
	settings := Settings{}
	err := s.db.QueryRow(ctx,
		`SELECT daily_goal, country FROM activity_settings WHERE user_id = $1`, userID).
		Scan(&settings.DailyGoal, &settings.Country)
	if errors.Is(err, pgx.ErrNoRows) {
		return Settings{DailyGoal: DefaultDailyGoal, IsDefault: true}, nil
	}
	return settings, err
}

func (s *Store) SaveSettings(ctx context.Context, userID string, dailyGoal int, country *string) error {
	_, err := s.db.Exec(ctx,
		`INSERT INTO activity_settings (user_id, daily_goal, country) VALUES ($1, $2, $3)
		 ON CONFLICT (user_id) DO UPDATE
		 SET daily_goal = EXCLUDED.daily_goal, country = EXCLUDED.country, updated_at = now()`,
		userID, dailyGoal, country)
	return err
}

// CountryRank answers "where do I stand in my country between these dates?".
// It only ever produces this user's own position: no names and no other counts.
func (s *Store) CountryRank(ctx context.Context, userID, country string, from, to time.Time) (rank, total int, err error) {
	err = s.db.QueryRow(ctx,
		`WITH totals AS (
		     SELECT d.user_id, sum(d.steps) AS steps
		     FROM (
		         SELECT e.user_id, e.day, max(e.steps) AS steps
		         FROM step_entries e
		         JOIN activity_settings s ON s.user_id = e.user_id
		         WHERE s.country = $1 AND e.day BETWEEN $2 AND $3
		         GROUP BY e.user_id, e.day
		     ) d
		     GROUP BY d.user_id
		 )
		 SELECT (SELECT count(*) FROM totals WHERE steps > COALESCE((SELECT steps FROM totals WHERE user_id = $4), 0)) + 1,
		        (SELECT count(*) FROM totals)`,
		country, from, to, userID).Scan(&rank, &total)
	if err != nil {
		return 0, 0, err
	}
	if rank > total { // this user has no steps yet, so they are not among the totals
		total = rank
	}
	return rank, total, nil
}

// Totals adds up many people's steps over a date range for the social service.
// People with no steps in the range are simply not in the result.
func (s *Store) Totals(ctx context.Context, userIDs []string, from, to, revealBefore time.Time) ([]UserTotal, error) {
	rows, err := s.db.Query(ctx,
		`SELECT d.user_id,
		        sum(d.steps)::bigint,
		        COALESCE(sum(d.steps) FILTER (WHERE d.day < $4), 0)::bigint,
		        count(*) FILTER (WHERE d.steps > 0),
		        max(d.steps),
		        count(*) FILTER (WHERE d.steps >= COALESCE(s.daily_goal, $5))
		 FROM (
		     SELECT user_id, day, max(steps) AS steps
		     FROM step_entries
		     WHERE user_id = ANY($1::text[]::uuid[])
		       AND day BETWEEN $2 AND $3
		     GROUP BY user_id, day
		 ) d
		 LEFT JOIN activity_settings s ON s.user_id = d.user_id
		 GROUP BY d.user_id`,
		userIDs, from, to, revealBefore, DefaultDailyGoal)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	totals := []UserTotal{}
	for rows.Next() {
		var t UserTotal
		if err := rows.Scan(&t.UserID, &t.Steps, &t.RevealedSteps, &t.DaysActive, &t.BestDay, &t.DaysGoalMet); err != nil {
			return nil, err
		}
		totals = append(totals, t)
	}
	return totals, rows.Err()
}

// DeleteUser removes everything this service knows about a person.
func (s *Store) DeleteUser(ctx context.Context, userID string) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `DELETE FROM step_entries WHERE user_id = $1`, userID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM activity_settings WHERE user_id = $1`, userID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
