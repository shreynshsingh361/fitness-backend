// Package store holds every SQL query the nutrition service runs.
package store

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DefaultCalorieGoal is used until the user sets their own.
const DefaultCalorieGoal = 2000

var (
	ErrNotFound    = errors.New("not found")
	ErrUnknownFood = errors.New("unknown food")
)

type Store struct {
	db *pgxpool.Pool
}

// Ping reports whether the database answers. The readiness check uses it.
func (s *Store) Ping(ctx context.Context) error { return s.db.Ping(ctx) }

func New(db *pgxpool.Pool) *Store { return &Store{db: db} }

// ---------- types ----------

type Food struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Brand    string  `json:"brand"`  // empty for generic foods
	Source   string  `json:"source"` // hxvy, usda (USDA FoodData Central), off (Open Food Facts), indb (Indian Nutrient Databank) or recipe
	Serving  string  `json:"serving"`
	Calories int     `json:"calories"`
	ProteinG float64 `json:"protein_g"`
	CarbsG   float64 `json:"carbs_g"`
	FatG     float64 `json:"fat_g"`
}

type Entry struct {
	ID        string    `json:"id"`
	EatenOn   string    `json:"eaten_on"` // YYYY-MM-DD
	Meal      string    `json:"meal"`
	FoodID    *string   `json:"food_id"`
	FoodName  string    `json:"food_name"`
	Serving   string    `json:"serving"`
	Servings  float64   `json:"servings"`
	Calories  int       `json:"calories"` // already multiplied by servings
	ProteinG  float64   `json:"protein_g"`
	CarbsG    float64   `json:"carbs_g"`
	FatG      float64   `json:"fat_g"`
	CreatedAt time.Time `json:"created_at"`
}

type NewEntry struct {
	ClientOpID string
	FoodID     string
	Meal       string
	Servings   float64
	EatenOn    time.Time
}

type Totals struct {
	Calories int     `json:"calories"`
	ProteinG float64 `json:"protein_g"`
	CarbsG   float64 `json:"carbs_g"`
	FatG     float64 `json:"fat_g"`
}

// DaySummary is what the dashboard shows for one day.
type DaySummary struct {
	Date          string  `json:"date"`
	CaloriesEaten int     `json:"calories_eaten"`
	CaloriesGoal  int     `json:"calories_goal"`
	ProteinG      float64 `json:"protein_g"`
	CarbsG        float64 `json:"carbs_g"`
	FatG          float64 `json:"fat_g"`
	WaterMl       int     `json:"water_ml"`
	EntryCount    int     `json:"entry_count"`
}

// ---------- foods ----------

// MaxFoodTotal caps the "total" of a search. Counting every match among millions of
// products would be slow, and nobody pages through more than this anyway.
const MaxFoodTotal = 1000

// minPackagedSearch is the shortest search that also looks through packaged products.
// One or two letters would match millions of them, so those search generic foods only.
const minPackagedSearch = 3

// visibleFood is true for the catalogue's foods and the user's own recipes, never for
// someone else's recipe. $N is the user id.
func visibleFood(n int) string {
	return fmt.Sprintf(`(owner_id IS NULL OR owner_id = $%d)`, n)
}

// ListFoods searches the foods this user can see: the catalogue and their own recipes.
func (s *Store) ListFoods(ctx context.Context, userID, search string, limit, offset int) ([]Food, int, error) {
	search = strings.ToLower(search)
	packaged := len([]rune(search)) >= minPackagedSearch
	filter := `WHERE ($1::text = '' OR search_text LIKE '%' || $1::text || '%')
	             AND ($2 OR source <> 'off') AND ` + visibleFood(3)

	var total int
	if err := s.db.QueryRow(ctx,
		`SELECT count(*) FROM (SELECT 1 FROM foods `+filter+` LIMIT $4) AS matches`,
		search, packaged, userID, MaxFoodTotal).Scan(&total); err != nil {
		return nil, 0, err
	}

	// Best matches first:
	//  1. the user's own recipes, then our own everyday foods;
	//  2. a word that starts with what was typed, from any source ("dal" finds Dal
	//     products before USDA's beef meDALlion);
	//  3. Indian dishes (INDB), then USDA's generic foods, then packaged products;
	//  4. a name that starts with it, then shorter names.
	rows, err := s.db.Query(ctx,
		`SELECT id, name, brand, source, serving, calories, protein_g, carbs_g, fat_g FROM foods `+filter+`
		 ORDER BY source <> 'recipe', source <> 'hxvy',
		          ' ' || translate(search_text, ',()/-', '     ') NOT LIKE '% ' || $1::text || '%',
		          source = 'off', source <> 'indb', search_text NOT LIKE $1::text || '%', length(name), name
		 LIMIT $4 OFFSET $5`, search, packaged, userID, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	items := []Food{}
	for rows.Next() {
		var f Food
		if err := rows.Scan(&f.ID, &f.Name, &f.Brand, &f.Source, &f.Serving,
			&f.Calories, &f.ProteinG, &f.CarbsG, &f.FatG); err != nil {
			return nil, 0, err
		}
		items = append(items, f)
	}
	return items, total, rows.Err()
}

// ---------- diary ----------

const entryColumns = `id, eaten_on::text, meal, food_id, food_name, serving, servings,
                      calories, protein_g, carbs_g, fat_g, created_at`

func scanEntry(row pgx.Row) (Entry, error) {
	var e Entry
	err := row.Scan(&e.ID, &e.EatenOn, &e.Meal, &e.FoodID, &e.FoodName, &e.Serving, &e.Servings,
		&e.Calories, &e.ProteinG, &e.CarbsG, &e.FatG, &e.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Entry{}, ErrNotFound
	}
	return e, err
}

// CreateEntry adds a food to the diary. It copies the food's nutrition into the
// entry (a snapshot). The bool result is true if the entry is new, false if the
// same client_op_id was already saved (a retry): then the existing entry is
// returned and nothing new is written.
func (s *Store) CreateEntry(ctx context.Context, userID string, in NewEntry) (Entry, bool, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Entry{}, false, err
	}
	defer tx.Rollback(ctx)

	var food Food
	err = tx.QueryRow(ctx,
		`SELECT id, name, serving, calories, protein_g, carbs_g, fat_g FROM foods
		 WHERE id = $1 AND `+visibleFood(2), in.FoodID, userID).
		Scan(&food.ID, &food.Name, &food.Serving, &food.Calories, &food.ProteinG, &food.CarbsG, &food.FatG)
	if errors.Is(err, pgx.ErrNoRows) {
		return Entry{}, false, ErrUnknownFood
	}
	if err != nil {
		return Entry{}, false, err
	}
	calories, protein, carbs, fat := scaleFood(food, in.Servings)

	entry, err := scanEntry(tx.QueryRow(ctx,
		`INSERT INTO diary_entries
		     (user_id, client_op_id, eaten_on, meal, food_id, food_name, serving, servings,
		      calories, protein_g, carbs_g, fat_g)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		 ON CONFLICT (user_id, client_op_id) DO NOTHING
		 RETURNING `+entryColumns,
		userID, in.ClientOpID, in.EatenOn, in.Meal, food.ID, food.Name, food.Serving, in.Servings,
		calories, protein, carbs, fat))
	if errors.Is(err, ErrNotFound) {
		// Conflict: this exact request was already saved. Return that entry.
		existing, err := scanEntry(tx.QueryRow(ctx,
			`SELECT `+entryColumns+` FROM diary_entries WHERE user_id = $1 AND client_op_id = $2`,
			userID, in.ClientOpID))
		return existing, false, err
	}
	if err != nil {
		return Entry{}, false, err
	}
	return entry, true, tx.Commit(ctx)
}

// DiaryForDay returns one day's entries (oldest first) and their totals.
func (s *Store) DiaryForDay(ctx context.Context, userID string, day time.Time) ([]Entry, Totals, error) {
	rows, err := s.db.Query(ctx,
		`SELECT `+entryColumns+` FROM diary_entries
		 WHERE user_id = $1 AND eaten_on = $2 ORDER BY created_at`, userID, day)
	if err != nil {
		return nil, Totals{}, err
	}
	defer rows.Close()

	entries := []Entry{}
	var totals Totals
	for rows.Next() {
		e, err := scanEntry(rows)
		if err != nil {
			return nil, Totals{}, err
		}
		entries = append(entries, e)
		totals.Calories += e.Calories
		totals.ProteinG += e.ProteinG
		totals.CarbsG += e.CarbsG
		totals.FatG += e.FatG
	}
	totals.ProteinG, totals.CarbsG, totals.FatG = round1(totals.ProteinG), round1(totals.CarbsG), round1(totals.FatG)
	return entries, totals, rows.Err()
}

// DayCalories is one day of a month and the calories eaten on it.
type DayCalories struct {
	Date     string `json:"date"`
	Calories int    `json:"calories"`
}

// CaloriesInMonth is the calories eaten on each day of the month of [first] that has any
// food in the diary, oldest day first. Days with nothing logged are left out.
func (s *Store) CaloriesInMonth(ctx context.Context, userID string, first time.Time) ([]DayCalories, error) {
	rows, err := s.db.Query(ctx,
		`SELECT eaten_on, COALESCE(sum(calories), 0)::int FROM diary_entries
		 WHERE user_id = $1 AND eaten_on >= $2 AND eaten_on < $3
		 GROUP BY eaten_on ORDER BY eaten_on`, userID, first, first.AddDate(0, 1, 0))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	days := []DayCalories{}
	for rows.Next() {
		var day time.Time
		var d DayCalories
		if err := rows.Scan(&day, &d.Calories); err != nil {
			return nil, err
		}
		d.Date = day.Format("2006-01-02")
		days = append(days, d)
	}
	return days, rows.Err()
}

// DeleteEntry only deletes the user's own entries.
func (s *Store) DeleteEntry(ctx context.Context, userID, id string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM diary_entries WHERE id = $1 AND user_id = $2`, id, userID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ---------- goals ----------

// CalorieGoal returns the user's goal, or DefaultCalorieGoal if they never set one.
func (s *Store) CalorieGoal(ctx context.Context, userID string) (calories int, isDefault bool, err error) {
	err = s.db.QueryRow(ctx, `SELECT calories FROM nutrition_goals WHERE user_id = $1`, userID).Scan(&calories)
	if errors.Is(err, pgx.ErrNoRows) {
		return DefaultCalorieGoal, true, nil
	}
	return calories, false, err
}

func (s *Store) SetCalorieGoal(ctx context.Context, userID string, calories int) error {
	_, err := s.db.Exec(ctx,
		`INSERT INTO nutrition_goals (user_id, calories) VALUES ($1, $2)
		 ON CONFLICT (user_id) DO UPDATE SET calories = EXCLUDED.calories, updated_at = now()`,
		userID, calories)
	return err
}

// ---------- water ----------

// claimWaterOp records that this request is being applied. It says false when the same
// client_op_id was already applied (a retry), so the change must not be made twice. An empty
// id (an older app) is always applied.
func claimWaterOp(ctx context.Context, tx pgx.Tx, userID, opID string) (bool, error) {
	if opID == "" {
		return true, nil
	}
	tag, err := tx.Exec(ctx,
		`INSERT INTO water_ops (user_id, client_op_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`, userID, opID)
	return tag.RowsAffected() == 1, err
}

// AddWater logs water. opID makes a retry harmless.
func (s *Store) AddWater(ctx context.Context, userID string, day time.Time, ml int, opID string) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	fresh, err := claimWaterOp(ctx, tx, userID, opID)
	if err != nil || !fresh {
		return err
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO water_logs (user_id, logged_on, amount_ml) VALUES ($1, $2, $3)`, userID, day, ml); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) WaterTotal(ctx context.Context, userID string, day time.Time) (int, error) {
	var total int
	err := s.db.QueryRow(ctx,
		`SELECT COALESCE(sum(amount_ml), 0) FROM water_logs WHERE user_id = $1 AND logged_on = $2`,
		userID, day).Scan(&total)
	return total, err
}

func (s *Store) ClearWater(ctx context.Context, userID string, day time.Time) error {
	_, err := s.db.Exec(ctx, `DELETE FROM water_logs WHERE user_id = $1 AND logged_on = $2`, userID, day)
	return err
}

// RemoveWater takes ml off the day's water, newest logs first: a log the amount covers is
// deleted, and the last one it reaches is only shortened. Asking for more than the day has
// just empties it, so the total never goes below zero.
func (s *Store) RemoveWater(ctx context.Context, userID string, day time.Time, ml int, opID string) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if fresh, err := claimWaterOp(ctx, tx, userID, opID); err != nil || !fresh {
		return err
	}

	// FOR UPDATE makes two removals at the same moment take turns, so the second one
	// works from what the first left instead of from the same amounts.
	rows, err := tx.Query(ctx,
		`SELECT id, amount_ml FROM water_logs WHERE user_id = $1 AND logged_on = $2
		 ORDER BY created_at DESC, id DESC FOR UPDATE`, userID, day)
	if err != nil {
		return err
	}
	type waterLog struct {
		id string
		ml int
	}
	var logs []waterLog
	for rows.Next() {
		var l waterLog
		if err := rows.Scan(&l.id, &l.ml); err != nil {
			rows.Close()
			return err
		}
		logs = append(logs, l)
	}
	rows.Close() // the transaction's connection is free for the changes below only once this is closed
	if err := rows.Err(); err != nil {
		return err
	}

	for _, l := range logs {
		if ml == 0 {
			break
		}
		if l.ml <= ml {
			_, err = tx.Exec(ctx, `DELETE FROM water_logs WHERE id = $1`, l.id)
			ml -= l.ml
		} else {
			_, err = tx.Exec(ctx, `UPDATE water_logs SET amount_ml = amount_ml - $2 WHERE id = $1`, l.id, ml)
			ml = 0
		}
		if err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// ---------- summary ----------

func (s *Store) Summary(ctx context.Context, userID string, day time.Time) (DaySummary, error) {
	sum := DaySummary{Date: day.Format("2006-01-02")}

	err := s.db.QueryRow(ctx,
		`SELECT COALESCE(sum(calories), 0), COALESCE(sum(protein_g), 0),
		        COALESCE(sum(carbs_g), 0),  COALESCE(sum(fat_g), 0), count(*)
		 FROM diary_entries WHERE user_id = $1 AND eaten_on = $2`, userID, day).
		Scan(&sum.CaloriesEaten, &sum.ProteinG, &sum.CarbsG, &sum.FatG, &sum.EntryCount)
	if err != nil {
		return DaySummary{}, err
	}
	sum.ProteinG, sum.CarbsG, sum.FatG = round1(sum.ProteinG), round1(sum.CarbsG), round1(sum.FatG)

	if sum.CaloriesGoal, _, err = s.CalorieGoal(ctx, userID); err != nil {
		return DaySummary{}, err
	}
	if sum.WaterMl, err = s.WaterTotal(ctx, userID, day); err != nil {
		return DaySummary{}, err
	}
	return sum, nil
}

// DeleteUser removes the user's diary, goal, water logs and recipes in one transaction.
func (s *Store) DeleteUser(ctx context.Context, userID string) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	for _, statement := range []string{
		`DELETE FROM diary_entries WHERE user_id = $1`,
		`DELETE FROM nutrition_goals WHERE user_id = $1`,
		`DELETE FROM water_logs WHERE user_id = $1`,
		`DELETE FROM water_ops WHERE user_id = $1`,
		// Their recipes, with each one's ingredients (ON DELETE CASCADE).
		`DELETE FROM foods WHERE owner_id = $1`,
	} {
		if _, err := tx.Exec(ctx, statement, userID); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// ---------- helpers ----------

// scaleFood multiplies a food's nutrition by the number of servings eaten.
func scaleFood(f Food, servings float64) (calories int, protein, carbs, fat float64) {
	return int(math.Round(float64(f.Calories) * servings)),
		round1(f.ProteinG * servings),
		round1(f.CarbsG * servings),
		round1(f.FatG * servings)
}

func round1(x float64) float64 { return math.Round(x*10) / 10 }
