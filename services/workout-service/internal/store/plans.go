package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ---------- plans ----------
//
// A plan is one person's own list of exercises for a body part ("my back day"), with
// the sets they aim for: each set has its own weight and reps.

// BodyParts are the plans someone can have, in the order the app shows them.
var BodyParts = []string{"back", "biceps", "triceps", "forearms", "chest", "legs", "abs", "shoulders"}

const (
	MaxPlanExercises = 20
	MaxPlanSets      = 10
	DefaultPlanSets  = 3  // a new exercise starts with this many sets...
	DefaultPlanReps  = 10 // ...of this many reps, weight 0
)

var (
	ErrWrongBodyPart = errors.New("exercise is for another body part")
	ErrAlreadyInPlan = errors.New("exercise is already in the plan")
	ErrPlanFull      = errors.New("plan is full")
)

type PlanSet struct {
	Weight float64 `json:"weight"` // in the exercise's unit
	Reps   int     `json:"reps"`
}

type PlanExercise struct {
	ID       string    `json:"id"`
	Exercise Exercise  `json:"exercise"`
	Unit     string    `json:"unit"` // kg or lb
	Sets     []PlanSet `json:"sets"`
}

type PlanSummary struct {
	BodyPart      string `json:"body_part"`
	ExerciseCount int    `json:"exercise_count"`
}

// PlanSummaries says how many exercises each of the user's plans has, for every body
// part (0 for the ones they have not started).
func (s *Store) PlanSummaries(ctx context.Context, userID string) ([]PlanSummary, error) {
	rows, err := s.db.Query(ctx,
		`SELECT body_part, count(*) FROM plan_exercises WHERE user_id = $1 GROUP BY body_part`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := map[string]int{}
	for rows.Next() {
		var part string
		var n int
		if err := rows.Scan(&part, &n); err != nil {
			return nil, err
		}
		counts[part] = n
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]PlanSummary, len(BodyParts))
	for i, part := range BodyParts {
		out[i] = PlanSummary{BodyPart: part, ExerciseCount: counts[part]}
	}
	return out, nil
}

// Plan lists the exercises of one of the user's plans, in the order they were added.
func (s *Store) Plan(ctx context.Context, userID, bodyPart string) ([]PlanExercise, error) {
	return s.planExercises(ctx, `pe.user_id = $1 AND pe.body_part = $2`, userID, bodyPart)
}

func (s *Store) planExercises(ctx context.Context, where string, args ...any) ([]PlanExercise, error) {
	rows, err := s.db.Query(ctx,
		`SELECT pe.id, pe.unit, e.id, e.name, e.muscle_group, e.equipment, e.body_part, e.motion, e.muscles
		 FROM plan_exercises pe JOIN exercises e ON e.id = pe.exercise_id
		 WHERE `+where+` ORDER BY pe.position`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	list := []PlanExercise{}
	at := map[string]int{}
	for rows.Next() {
		p := PlanExercise{Sets: []PlanSet{}}
		e := &p.Exercise
		if err := rows.Scan(&p.ID, &p.Unit, &e.ID, &e.Name, &e.MuscleGroup, &e.Equipment, &e.BodyPart,
			&e.Motion, &e.Muscles); err != nil {
			return nil, err
		}
		at[p.ID] = len(list)
		list = append(list, p)
	}
	if err := rows.Err(); err != nil || len(list) == 0 {
		return list, err
	}

	ids := make([]string, len(list))
	for i, p := range list {
		ids[i] = p.ID
	}
	rows, err = s.db.Query(ctx,
		`SELECT plan_exercise_id, weight, reps FROM plan_sets
		 WHERE plan_exercise_id = ANY($1::text[]::uuid[]) ORDER BY plan_exercise_id, position`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var set PlanSet
		if err := rows.Scan(&id, &set.Weight, &set.Reps); err != nil {
			return nil, err
		}
		p := &list[at[id]]
		p.Sets = append(p.Sets, set)
	}
	return list, rows.Err()
}

func (s *Store) planExercise(ctx context.Context, userID, bodyPart, id string) (PlanExercise, error) {
	list, err := s.planExercises(ctx, `pe.user_id = $1 AND pe.body_part = $2 AND pe.id = $3`, userID, bodyPart, id)
	if err != nil {
		return PlanExercise{}, err
	}
	if len(list) == 0 {
		return PlanExercise{}, ErrNotFound
	}
	return list[0], nil
}

// AddToPlan puts an exercise at the end of a plan, with DefaultPlanSets empty sets.
// Errors: ErrUnknownExercise, ErrWrongBodyPart, ErrAlreadyInPlan and ErrPlanFull.
func (s *Store) AddToPlan(ctx context.Context, userID, bodyPart, exerciseID string) (PlanExercise, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return PlanExercise{}, err
	}
	defer tx.Rollback(ctx)

	var part string
	err = tx.QueryRow(ctx, `SELECT body_part FROM exercises WHERE id = $1`, exerciseID).Scan(&part)
	if errors.Is(err, pgx.ErrNoRows) {
		return PlanExercise{}, ErrUnknownExercise
	}
	if err != nil {
		return PlanExercise{}, err
	}
	if part != bodyPart {
		return PlanExercise{}, ErrWrongBodyPart
	}

	var count, last int
	if err := tx.QueryRow(ctx,
		`SELECT count(*), COALESCE(max(position), 0) FROM plan_exercises WHERE user_id = $1 AND body_part = $2`,
		userID, bodyPart).Scan(&count, &last); err != nil {
		return PlanExercise{}, err
	}
	if count >= MaxPlanExercises {
		return PlanExercise{}, ErrPlanFull
	}

	var id string
	err = tx.QueryRow(ctx,
		`INSERT INTO plan_exercises (user_id, body_part, exercise_id, position) VALUES ($1, $2, $3, $4)
		 RETURNING id`, userID, bodyPart, exerciseID, last+1).Scan(&id)
	if isUniqueViolation(err) {
		return PlanExercise{}, ErrAlreadyInPlan
	}
	if err != nil {
		return PlanExercise{}, err
	}
	sets := make([]PlanSet, DefaultPlanSets)
	for i := range sets {
		sets[i] = PlanSet{Weight: 0, Reps: DefaultPlanReps}
	}
	if err := insertSets(ctx, tx, id, sets); err != nil {
		return PlanExercise{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return PlanExercise{}, err
	}
	return s.planExercise(ctx, userID, bodyPart, id)
}

// UpdatePlanExercise replaces the unit and all the sets of one exercise in a plan.
func (s *Store) UpdatePlanExercise(ctx context.Context, userID, bodyPart, id, unit string, sets []PlanSet) (PlanExercise, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return PlanExercise{}, err
	}
	defer tx.Rollback(ctx)

	tag, err := tx.Exec(ctx, `UPDATE plan_exercises SET unit = $4 WHERE id = $1 AND user_id = $2 AND body_part = $3`,
		id, userID, bodyPart, unit)
	if err != nil {
		return PlanExercise{}, err
	}
	if tag.RowsAffected() == 0 {
		return PlanExercise{}, ErrNotFound
	}
	if _, err := tx.Exec(ctx, `DELETE FROM plan_sets WHERE plan_exercise_id = $1`, id); err != nil {
		return PlanExercise{}, err
	}
	if err := insertSets(ctx, tx, id, sets); err != nil {
		return PlanExercise{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return PlanExercise{}, err
	}
	return s.planExercise(ctx, userID, bodyPart, id)
}

// RemoveFromPlan takes an exercise, with its sets, out of a plan.
func (s *Store) RemoveFromPlan(ctx context.Context, userID, bodyPart, id string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM plan_exercises WHERE id = $1 AND user_id = $2 AND body_part = $3`,
		id, userID, bodyPart)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func insertSets(ctx context.Context, tx pgx.Tx, planExerciseID string, sets []PlanSet) error {
	for i, set := range sets {
		if _, err := tx.Exec(ctx,
			`INSERT INTO plan_sets (plan_exercise_id, position, weight, reps) VALUES ($1, $2, $3, $4)`,
			planExerciseID, i+1, set.Weight, set.Reps); err != nil {
			return err
		}
	}
	return nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
