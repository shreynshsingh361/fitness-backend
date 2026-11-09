package api

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	"fitness/nutrition-service/internal/store"
)

// openFoodFacts is asked only for a barcode the catalogue does not hold. It is a variable
// so tests can answer without the network.
var openFoodFacts = fetchOpenFoodFacts

var offClient = &http.Client{Timeout: 5 * time.Second}

// errNoNutrition means the product exists but lists no calories or macros, so there is
// nothing honest to log.
var errNoNutrition = fmt.Errorf("the product has no nutrition information")

type offResponse struct {
	Status  int `json:"status"`
	Product struct {
		Name            string          `json:"product_name"`
		Brands          string          `json:"brands"`
		ServingSize     string          `json:"serving_size"`
		ServingQuantity json.RawMessage `json:"serving_quantity"`
		Nutriments      map[string]any  `json:"nutriments"`
	} `json:"product"`
}

// fetchOpenFoodFacts looks one barcode up. A nil food with a nil error means Open Food
// Facts does not know it.
func fetchOpenFoodFacts(ctx context.Context, code string) (*store.Food, error) {
	u := "https://world.openfoodfacts.org/api/v2/product/" + url.PathEscape(code) +
		"?fields=product_name,brands,serving_size,serving_quantity,nutriments"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Hxvy/1.0 (personal learning project)")
	res, err := offClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("open food facts answered %d", res.StatusCode)
	}
	var body offResponse
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		return nil, err
	}
	if body.Status != 1 {
		return nil, nil
	}
	return foodFromOFF(body)
}

// foodFromOFF turns Open Food Facts' per-100 g numbers into the app's per-serving ones,
// the same way the bulk import does: the serving on the label if it gives its weight,
// otherwise 100 g.
func foodFromOFF(body offResponse) (*store.Food, error) {
	p := body.Product
	name := strings.Join(strings.Fields(p.Name), " ")
	if name == "" {
		return nil, errNoNutrition
	}
	kcal, ok1 := offNumber(p.Nutriments, "energy-kcal_100g")
	protein, ok2 := offNumber(p.Nutriments, "proteins_100g")
	carbs, ok3 := offNumber(p.Nutriments, "carbohydrates_100g")
	fat, ok4 := offNumber(p.Nutriments, "fat_100g")
	if !ok1 || !ok2 || !ok3 || !ok4 {
		return nil, errNoNutrition
	}
	// Calories should roughly match the macronutrients (4/4/9 kcal per gram).
	if math.Abs(4*protein+4*carbs+9*fat-kcal) > math.Max(40, kcal*0.5) {
		return nil, errNoNutrition
	}

	serving, grams := "100 g", 100.0
	var quantity float64
	if json.Unmarshal(p.ServingQuantity, &quantity) != nil {
		var asText string
		if json.Unmarshal(p.ServingQuantity, &asText) == nil {
			fmt.Sscanf(asText, "%f", &quantity)
		}
	}
	if label := strings.Join(strings.Fields(p.ServingSize), " "); label != "" && quantity >= 1 && quantity <= 2000 {
		serving, grams = label, quantity
	}
	brand, _, _ := strings.Cut(p.Brands, ",")
	scale := grams / 100
	return &store.Food{
		Name:     truncate(name, 200),
		Brand:    truncate(strings.TrimSpace(brand), 100),
		Source:   "off",
		Serving:  truncate(serving, 80),
		Calories: int(math.Round(kcal * scale)),
		ProteinG: math.Round(protein*scale*10) / 10,
		CarbsG:   math.Round(carbs*scale*10) / 10,
		FatG:     math.Round(fat*scale*10) / 10,
	}, nil
}

func offNumber(m map[string]any, key string) (float64, bool) {
	switch v := m[key].(type) {
	case float64:
		return v, v >= 0
	case string:
		var f float64
		_, err := fmt.Sscanf(v, "%f", &f)
		return f, err == nil && f >= 0
	}
	return 0, false
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
