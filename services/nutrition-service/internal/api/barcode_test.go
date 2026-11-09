package api

import (
	"encoding/json"
	"net/http"
	"slices"
	"testing"

	"fitness/nutrition-service/internal/store"
)

func TestBarcodeMustBeEightToFourteenDigits(t *testing.T) {
	user := map[string]string{"X-User-ID": someUser}
	for _, code := range []string{"1234567", "123456789012345", "12345abc90123", "12345678x"} {
		if got := do("GET", "/api/v1/foods/barcode/"+code, "", user).Code; got != http.StatusUnprocessableEntity {
			t.Errorf("barcode %q: status = %d, want 422", code, got)
		}
	}
	if got := do("GET", "/api/v1/foods/barcode/8901058000290", "", nil).Code; got != http.StatusUnauthorized {
		t.Errorf("without a user: status = %d, want 401", got)
	}
}

// A UPC-A and the EAN-13 with a 0 in front are one product, however the catalogue spelled it.
func TestBarcodeCandidatesCoverEveryLength(t *testing.T) {
	for _, code := range []string{"011110531568", "0011110531568", "11110531568", "00011110531568"} {
		got := store.BarcodeCandidates(code)
		for _, want := range []string{"011110531568", "0011110531568"} {
			if !slices.Contains(got, want) {
				t.Errorf("%s: candidates %v lack %s", code, got, want)
			}
		}
	}
	if got := store.BarcodeCandidates("00087186"); !slices.Contains(got, "00087186") || !slices.Contains(got, "0000000000087186"[2:]) {
		t.Errorf("EAN-8 candidates %v", got)
	}
}

func offBody(t *testing.T, js string) offResponse {
	t.Helper()
	var body offResponse
	if err := json.Unmarshal([]byte(js), &body); err != nil {
		t.Fatal(err)
	}
	return body
}

func TestOpenFoodFactsProductBecomesPerServing(t *testing.T) {
	body := offBody(t, `{"status":1,"product":{"product_name":"  Maggi  Masala ","brands":"Nestle, Maggi",
		"serving_size":"70 g","serving_quantity":"70",
		"nutriments":{"energy-kcal_100g":427,"proteins_100g":9.2,"carbohydrates_100g":60.1,"fat_100g":16.4}}}`)
	food, err := foodFromOFF(body)
	if err != nil {
		t.Fatal(err)
	}
	if food.Name != "Maggi Masala" || food.Brand != "Nestle" || food.Serving != "70 g" || food.Source != "off" {
		t.Errorf("unexpected food %+v", food)
	}
	if food.Calories != 299 || food.ProteinG != 6.4 || food.CarbsG != 42.1 || food.FatG != 11.5 {
		t.Errorf("per-serving numbers wrong: %+v", food)
	}
}

func TestOpenFoodFactsWithoutServingWeightIsPer100g(t *testing.T) {
	body := offBody(t, `{"status":1,"product":{"product_name":"Biscuit","serving_size":"1 biscuit","serving_quantity":null,
		"nutriments":{"energy-kcal_100g":480,"proteins_100g":7,"carbohydrates_100g":70,"fat_100g":19}}}`)
	food, err := foodFromOFF(body)
	if err != nil || food.Serving != "100 g" || food.Calories != 480 {
		t.Errorf("food %+v, err %v", food, err)
	}
}

// A product with no, or nonsense, nutrition must not be logged as if it had some.
func TestOpenFoodFactsWithoutHonestNutritionIsRefused(t *testing.T) {
	for name, js := range map[string]string{
		"no calories":        `{"status":1,"product":{"product_name":"X","nutriments":{"proteins_100g":1,"carbohydrates_100g":1,"fat_100g":1}}}`,
		"no name":            `{"status":1,"product":{"product_name":"","nutriments":{"energy-kcal_100g":100,"proteins_100g":1,"carbohydrates_100g":20,"fat_100g":1}}}`,
		"adds up to nothing": `{"status":1,"product":{"product_name":"X","nutriments":{"energy-kcal_100g":900,"proteins_100g":0,"carbohydrates_100g":1,"fat_100g":0}}}`,
	} {
		if _, err := foodFromOFF(offBody(t, js)); err != errNoNutrition {
			t.Errorf("%s: err = %v, want errNoNutrition", name, err)
		}
	}
}
