#!/usr/bin/env bash
# Loads a foods CSV (made by import_foods.py) into the nutrition database. Safe to repeat:
# a food that is already there is updated, not added twice.
#
#   ./scripts/load-foods.sh data/foods/foods.india.csv
#   COMPOSE_FILE=docker-compose.prod.yml ./scripts/load-foods.sh foods.india.csv   # on the server
set -euo pipefail
cd "$(dirname "$0")/.."
CSV="${1:?usage: $0 <foods csv>}"
[ -s "$CSV" ] || { echo "no such file: $CSV" >&2; exit 1; }

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
