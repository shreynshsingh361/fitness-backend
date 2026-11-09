package store

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
)

// BarcodeCandidates is every way the catalogue might spell one barcode. A UPC-A (12
// digits) is the same product as the EAN-13 with a 0 in front, and Open Food Facts keeps
// whichever length it was first given, so a scan is looked up under all of them. They are
// exact matches on an indexed column, which keeps the lookup fast over millions of rows.
func BarcodeCandidates(code string) []string {
	trimmed := strings.TrimLeft(code, "0")
	seen := map[string]bool{code: true}
	out := []string{code}
	for _, width := range []int{8, 12, 13, 14} {
		if len(trimmed) > width {
			continue
		}
		padded := strings.Repeat("0", width-len(trimmed)) + trimmed
		if !seen[padded] {
			seen[padded] = true
			out = append(out, padded)
		}
	}
	return out
}

// FoodByBarcode finds a packaged product the catalogue already holds. found is false when
// it does not.
func (s *Store) FoodByBarcode(ctx context.Context, code string) (food Food, found bool, err error) {
	err = s.db.QueryRow(ctx,
		`SELECT id, name, brand, source, serving, calories, protein_g, carbs_g, fat_g FROM foods
		 WHERE source = 'off' AND source_id = ANY($1) LIMIT 1`, BarcodeCandidates(code)).
		Scan(&food.ID, &food.Name, &food.Brand, &food.Source, &food.Serving,
			&food.Calories, &food.ProteinG, &food.CarbsG, &food.FatG)
	if errors.Is(err, pgx.ErrNoRows) {
		return Food{}, false, nil
	}
	if err != nil {
		return Food{}, false, err
	}
	return food, true, nil
}

// SaveScannedFood keeps a product fetched from Open Food Facts, so the next scan of it (by
// anyone) is found locally. If it was saved in the meantime, that one is returned.
func (s *Store) SaveScannedFood(ctx context.Context, code string, f Food) (Food, error) {
	_, err := s.db.Exec(ctx,
		`INSERT INTO foods (source, source_id, name, brand, serving, calories, protein_g, carbs_g, fat_g)
		 VALUES ('off', $1, $2, $3, $4, $5, $6, $7, $8)
		 ON CONFLICT (source, source_id) WHERE source_id IS NOT NULL DO NOTHING`,
		code, f.Name, f.Brand, f.Serving, f.Calories, f.ProteinG, f.CarbsG, f.FatG)
	if err != nil {
		return Food{}, err
	}
	saved, found, err := s.FoodByBarcode(ctx, code)
	if err != nil {
		return Food{}, err
	}
	if !found {
		return Food{}, ErrNotFound
	}
	return saved, nil
}
