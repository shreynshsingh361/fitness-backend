#!/usr/bin/env bash
# Downloads USDA FoodData Central and Open Food Facts, then loads them into the
# nutrition service's foods table. Safe to run again: existing foods are updated, not
# duplicated, and diary entries keep their own copy of the numbers.
#
#   ./scripts/import-foods.sh              # USDA and all of Open Food Facts (about 3.3 million products, 1.2 GB)
#   ./scripts/import-foods.sh --india-only # USDA and only the Open Food Facts products made or sold in
#                                          # India (about 20 thousand rows). Use this on the real server.
#   ./scripts/import-foods.sh --usda-only
#   ./scripts/import-foods.sh --indb-only  # only the Indian dishes (a few seconds); the others do it too
#
# All of them also load the Indian Nutrient Databank: about 1,000 Indian dishes (dal tadka, idli,
# aloo gobi, biryani...) by Vijayakumar et al., 2024, CC BY 4.0. It must be credited in the app.
#
# Needs the stack running (docker compose up -d) so migration 003 has added the columns.
set -euo pipefail
cd "$(dirname "$0")/.."

DATA=data/foods
mkdir -p "$DATA"
USDA_FILES="FoodData_Central_sr_legacy_food_json_2018-04.zip FoodData_Central_foundation_food_json_2025-04-24.zip"
OFF_FILE=en.openfoodfacts.org.products.csv.gz
INDB_FILE=INDB.xlsx
INDB_URL="https://raw.githubusercontent.com/lindsayjaacks/Indian-Nutrient-Databank-INDB-/main/$INDB_FILE"

[ -s "$DATA/$INDB_FILE" ] || curl -fL --retry 5 -o "$DATA/$INDB_FILE" "$INDB_URL"
if [ "${1:-}" != "--indb-only" ]; then
  for f in $USDA_FILES; do
    [ -s "$DATA/$f" ] || curl -fL --retry 5 -C - -o "$DATA/$f" "https://fdc.nal.usda.gov/fdc-datasets/$f"
  done
fi
OFF_ARGS=""
case "${1:-}" in
  ""|--india-only)
    [ -s "$DATA/$OFF_FILE" ] || curl -fL --retry 5 -C - -o "$DATA/$OFF_FILE" "https://static.openfoodfacts.org/data/$OFF_FILE"
    OFF_ARGS="--off $DATA/$OFF_FILE"
    [ "${1:-}" = "--india-only" ] && OFF_ARGS="$OFF_ARGS --india-only"
    ;;
  --usda-only|--indb-only) ;;
  *) echo "unknown option: $1 (use --india-only, --usda-only or --indb-only)" >&2; exit 2 ;;
esac

USDA_ARGS=""
if [ "${1:-}" != "--indb-only" ]; then
  for f in $USDA_FILES; do USDA_ARGS="$USDA_ARGS $DATA/$f"; done
fi

# Convert everything first. Only a complete file is loaded, so a failure part-way
# through cannot leave a half-finished import behind.
# One file per mode, so a quick run never overwrites the big one from a full import.
case "${1:-}" in --indb-only) CSV="$DATA/foods.indb.csv" ;; --india-only) CSV="$DATA/foods.india.csv" ;; *) CSV="$DATA/foods.csv" ;; esac
# shellcheck disable=SC2086
python3 scripts/import_foods.py --indb "$DATA/$INDB_FILE" --usda $USDA_ARGS $OFF_ARGS > "$CSV.part"
mv "$CSV.part" "$CSV"

docker compose exec -T postgres sh -c 'psql -v ON_ERROR_STOP=1 -q -U "$POSTGRES_USER" -d nutrition_db \
    -c "CREATE TEMP TABLE staging (source text, source_id text, name text, brand text, serving text,
                                   calories integer, protein_g double precision, carbs_g double precision,
                                   fat_g double precision)" \
    -c "COPY staging FROM STDIN WITH (FORMAT csv, FORCE_NOT_NULL (brand))" \
    -c "INSERT INTO foods (source, source_id, name, brand, serving, calories, protein_g, carbs_g, fat_g)
        SELECT source, source_id, name, brand, serving, calories, protein_g, carbs_g, fat_g FROM staging
        ON CONFLICT (source, source_id) WHERE source_id IS NOT NULL DO UPDATE SET
          name = EXCLUDED.name, brand = EXCLUDED.brand, serving = EXCLUDED.serving,
          calories = EXCLUDED.calories, protein_g = EXCLUDED.protein_g,
          carbs_g = EXCLUDED.carbs_g, fat_g = EXCLUDED.fat_g" \
    -c "ANALYZE foods" \
    -c "SELECT source, count(*) FROM foods GROUP BY source ORDER BY source"' < "$CSV"
