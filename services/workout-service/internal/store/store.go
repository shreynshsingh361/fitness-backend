// Package store holds every SQL query the workout service runs.
package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrNotFound        = errors.New("not found")
	ErrUnknownExercise = errors.New("unknown exercise")
)

type Store struct {
	db *pgxpool.Pool
}

// Ping reports whether the database answers. The readiness check uses it.
func (s *Store) Ping(ctx context.Context) error { return s.db.Ping(ctx) }

func New(db *pgxpool.Pool) *Store { return &Store{db: db} }

// ---------- types ----------

type Exercise struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	MuscleGroup string `json:"muscle_group"`
	Equipment   string `json:"equipment"`
	BodyPart    string `json:"body_part"` // back, biceps, triceps, chest, legs, abs or shoulders
	Motion      string `json:"motion"`    // which animation the app plays, or "none"
	Muscles     string `json:"muscles"`   // what it trains, in words
}

const exerciseColumns = `id, name, muscle_group, equipment, body_part, motion, muscles`

func scanExercise(row pgx.Row, e *Exercise) error {
	return row.Scan(&e.ID, &e.Name, &e.MuscleGroup, &e.Equipment, &e.BodyPart, &e.Motion, &e.Muscles)
}

type Set struct {
	ID           string  `json:"id"`
	ExerciseID   string  `json:"exercise_id"`
	ExerciseName string  `json:"exercise_name"`
	Position     int     `json:"position"`
	Reps         int     `json:"reps"`
	WeightKg     float64 `json:"weight_kg"`
}

type Workout struct {
	ID              string    `json:"id"`
	Name            string    `json:"name"`
	Notes           string    `json:"notes"`
	StartedAt       time.Time `json:"started_at"`
	DurationMinutes int       `json:"duration_minutes"`
	CreatedAt       time.Time `json:"created_at"`
	SetCount        int       `json:"set_count"`
	VolumeKg        float64   `json:"volume_kg"` // total of reps x weight over all sets
	Sets            []Set     `json:"sets,omitempty"`
}

// NewWorkout is what the client sends to create a workout.
type NewWorkout struct {
	ClientOpID      string     `json:"client_op_id"`
	Name            string     `json:"name"`
	Notes           string     `json:"notes"`
	StartedAt       *time.Time `json:"started_at"`
	DurationMinutes int        `json:"duration_minutes"`
	Sets            []NewSet   `json:"sets"`
}

type NewSet struct {
	ExerciseID string  `json:"exercise_id"`
	Reps       int     `json:"reps"`
	WeightKg   float64 `json:"weight_kg"`
}

type Summary struct {
	Days          int        `json:"days"`
	Workouts      int        `json:"workouts"`
	TotalSets     int        `json:"total_sets"`
	VolumeKg      float64    `json:"volume_kg"`
	LastWorkoutAt *time.Time `json:"last_workout_at"`
}

type PersonalRecord struct {
	ExerciseID   string    `json:"exercise_id"`
	ExerciseName string    `json:"exercise_name"`
	WeightKg     float64   `json:"weight_kg"`
	Reps         int       `json:"reps"`
	AchievedAt   time.Time `json:"achieved_at"`
}

// ---------- exercises ----------

// ListExercises searches the library. Empty filters match everything.
func (s *Store) ListExercises(ctx context.Context, search, muscleGroup, bodyPart string, limit, offset int) ([]Exercise, int, error) {
	const filter = `WHERE ($1::text = '' OR name ILIKE '%' || $1::text || '%')
	                  AND ($2::text = '' OR muscle_group = $2::text)
	                  AND ($3::text = '' OR body_part = $3::text)`

	var total int
	if err := s.db.QueryRow(ctx, `SELECT count(*) FROM exercises `+filter, search, muscleGroup, bodyPart).
		Scan(&total); err != nil {
		return nil, 0, err
	}

	rows, err := s.db.Query(ctx,
		`SELECT `+exerciseColumns+` FROM exercises `+filter+`
		 ORDER BY name LIMIT $4 OFFSET $5`, search, muscleGroup, bodyPart, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	items := []Exercise{}
	for rows.Next() {
		var e Exercise
		if err := scanExercise(rows, &e); err != nil {
			return nil, 0, err
		}
		items = append(items, e)
	}
	return items, total, rows.Err()
}

// ---------- workouts ----------

// CreateWorkout saves a workout and all its sets in one transaction.
// The bool result is true if it was newly created, false if the same
// client_op_id was already saved (a retry), in which case the existing
// workout is returned and nothing new is written.
func (s *Store) CreateWorkout(ctx context.Context, userID string, in NewWorkout) (Workout, bool, error) {
	startedAt := time.Now().UTC()
	if in.StartedAt != nil {
		startedAt = in.StartedAt.UTC()
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Workout{}, false, err
	}
	defer tx.Rollback(ctx)

	var workoutID string
	err = tx.QueryRow(ctx,
		`INSERT INTO workouts (user_id, client_op_id, name, notes, started_at, duration_minutes)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 ON CONFLICT (user_id, client_op_id) DO NOTHING
		 RETURNING id`,
		userID, in.ClientOpID, in.Name, in.Notes, startedAt, in.DurationMinutes).Scan(&workoutID)

	if errors.Is(err, pgx.ErrNoRows) {
		// Conflict: this exact request was already saved. Return that workout.
		if err := tx.QueryRow(ctx,
			`SELECT id FROM workouts WHERE user_id = $1 AND client_op_id = $2`,
			userID, in.ClientOpID).Scan(&workoutID); err != nil {
			return Workout{}, false, err
		}
		w, err := s.GetWorkout(ctx, userID, workoutID)
		return w, false, err
	}
	if err != nil {
		return Workout{}, false, err
	}

	for i, set := range in.Sets {
		_, err := tx.Exec(ctx,
			`INSERT INTO workout_sets (workout_id, exercise_id, position, reps, weight_kg)
			 VALUES ($1, $2, $3, $4, $5)`,
			workoutID, set.ExerciseID, i+1, set.Reps, set.WeightKg)
		if isForeignKeyViolation(err) {
			return Workout{}, false, ErrUnknownExercise
		}
		if err != nil {
			return Workout{}, false, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return Workout{}, false, err
	}

	w, err := s.GetWorkout(ctx, userID, workoutID)
	return w, true, err
}

// GetWorkout returns one workout with its sets. It filters by user_id too,
// so asking for somebody else's workout looks exactly like "not found".
func (s *Store) GetWorkout(ctx context.Context, userID, id string) (Workout, error) {
	var w Workout
	err := s.db.QueryRow(ctx,
		`SELECT id, name, notes, started_at, duration_minutes, created_at
		 FROM workouts WHERE id = $1 AND user_id = $2`, id, userID).
		Scan(&w.ID, &w.Name, &w.Notes, &w.StartedAt, &w.DurationMinutes, &w.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Workout{}, ErrNotFound
	}
	if err != nil {
		return Workout{}, err
	}

	rows, err := s.db.Query(ctx,
		`SELECT s.id, s.exercise_id, e.name, s.position, s.reps, s.weight_kg
		 FROM workout_sets s JOIN exercises e ON e.id = s.exercise_id
		 WHERE s.workout_id = $1 ORDER BY s.position`, id)
	if err != nil {
		return Workout{}, err
	}
	defer rows.Close()

	w.Sets = []Set{}
	for rows.Next() {
		var set Set
		if err := rows.Scan(&set.ID, &set.ExerciseID, &set.ExerciseName, &set.Position, &set.Reps, &set.WeightKg); err != nil {
			return Workout{}, err
		}
		w.Sets = append(w.Sets, set)
		w.VolumeKg += float64(set.Reps) * set.WeightKg
	}
	w.SetCount = len(w.Sets)
	return w, rows.Err()
}

// ListWorkouts returns a page of the user's workouts, newest first.
// Sets are not included here (use GetWorkout); only their count and volume.
func (s *Store) ListWorkouts(ctx context.Context, userID string, limit, offset int) ([]Workout, int, error) {
	var total int
	if err := s.db.QueryRow(ctx, `SELECT count(*) FROM workouts WHERE user_id = $1`, userID).Scan(&total); err != nil {
		return nil, 0, err
	}

	rows, err := s.db.Query(ctx,
		`SELECT w.id, w.name, w.notes, w.started_at, w.duration_minutes, w.created_at,
		        count(s.id), COALESCE(sum(s.reps * s.weight_kg), 0)
		 FROM workouts w LEFT JOIN workout_sets s ON s.workout_id = w.id
		 WHERE w.user_id = $1
		 GROUP BY w.id
		 ORDER BY w.started_at DESC
		 LIMIT $2 OFFSET $3`, userID, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	items := []Workout{}
	for rows.Next() {
		var w Workout
		if err := rows.Scan(&w.ID, &w.Name, &w.Notes, &w.StartedAt, &w.DurationMinutes, &w.CreatedAt,
			&w.SetCount, &w.VolumeKg); err != nil {
			return nil, 0, err
		}
		items = append(items, w)
	}
	return items, total, rows.Err()
}

func (s *Store) DeleteWorkout(ctx context.Context, userID, id string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM workouts WHERE id = $1 AND user_id = $2`, id, userID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ---------- summary & records ----------

func (s *Store) Summary(ctx context.Context, userID string, days int) (Summary, error) {
	sum := Summary{Days: days}
	err := s.db.QueryRow(ctx,
		`SELECT count(DISTINCT w.id), count(s.id), COALESCE(sum(s.reps * s.weight_kg), 0), max(w.started_at)
		 FROM workouts w LEFT JOIN workout_sets s ON s.workout_id = w.id
		 WHERE w.user_id = $1 AND w.started_at >= now() - make_interval(days => $2::int)`,
		userID, days).Scan(&sum.Workouts, &sum.TotalSets, &sum.VolumeKg, &sum.LastWorkoutAt)
	return sum, err
}

// DeleteUser removes this user's workouts and plans; their sets go too (ON DELETE CASCADE).
func (s *Store) DeleteUser(ctx context.Context, userID string) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	for _, table := range []string{"workouts", "plan_exercises"} {
		if _, err := tx.Exec(ctx, `DELETE FROM `+table+` WHERE user_id = $1`, userID); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// PersonalRecords returns the heaviest set the user has ever logged per exercise.
func (s *Store) PersonalRecords(ctx context.Context, userID string) ([]PersonalRecord, error) {
	rows, err := s.db.Query(ctx,
		`SELECT * FROM (
		     SELECT DISTINCT ON (s.exercise_id)
		            s.exercise_id, e.name, s.weight_kg, s.reps, w.started_at
		     FROM workout_sets s
		     JOIN workouts  w ON w.id = s.workout_id
		     JOIN exercises e ON e.id = s.exercise_id
		     WHERE w.user_id = $1
		     ORDER BY s.exercise_id, s.weight_kg DESC, s.reps DESC, w.started_at DESC
		 ) best ORDER BY name`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	records := []PersonalRecord{}
	for rows.Next() {
		var r PersonalRecord
		if err := rows.Scan(&r.ExerciseID, &r.ExerciseName, &r.WeightKg, &r.Reps, &r.AchievedAt); err != nil {
			return nil, err
		}
		records = append(records, r)
	}
	return records, rows.Err()
}

func isForeignKeyViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23503"
}
