#!/usr/bin/env python3
"""Turns the USDA and Open Food Facts downloads into one CSV for the foods table.

Writes rows to stdout as: source, source_id, name, brand, serving, calories, protein_g,
carbs_g, fat_g. Nutrition is per one serving, the same as the foods the app started with.
Only the Python standard library is used.

    python3 scripts/import_foods.py --usda data/foods/*.zip --off data/foods/en.openfoodfacts.org.products.csv.gz
    python3 scripts/import_foods.py --usda data/foods/*.zip --off data/foods/en.openfoodfacts.org.products.csv.gz --india-only

Run it through scripts/import-foods.sh, which also loads the rows into the database.
"""

import argparse
import csv
import gzip
import json
import sys
import zipfile

from xlsx_reader import rows as xlsx_rows

# USDA nutrient ids.
ENERGY_KCAL = (1008, 2047, 2048)  # Foundation foods often only have the 2047/2048 variants.
PROTEIN, FAT, CARBS = 1003, 1004, 1005


def plausible(kcal, protein, carbs, fat):
    """Drops rows whose numbers cannot be real (typing mistakes are common in open data)."""
    if not (0 <= kcal <= 900 and 0 <= protein <= 100 and 0 <= carbs <= 100 and 0 <= fat <= 100):
        return False
    if protein + carbs + fat > 105:
        return False
    # Calories should roughly match the macronutrients (4/4/9 kcal per gram).
    estimate = 4 * protein + 4 * carbs + 9 * fat
    return abs(estimate - kcal) <= max(40, kcal * 0.5)


def row(writer, source, source_id, name, brand, serving, per100, grams):
    kcal, protein, carbs, fat = per100
    if not plausible(kcal, protein, carbs, fat):
        return False
    scale = grams / 100
    writer.writerow([
        source, source_id, name[:200], brand[:100], serving[:80],
        round(kcal * scale), round(protein * scale, 1), round(carbs * scale, 1), round(fat * scale, 1),
    ])
    return True


# ---------- USDA FoodData Central (JSON inside a zip) ----------

def usda(path, writer):
    written = 0
    with zipfile.ZipFile(path) as archive:
        for member in archive.namelist():
            if not member.endswith(".json"):
                continue
            data = json.load(archive.open(member))
            foods = next(v for k, v in data.items() if isinstance(v, list))
            for food in foods:
                amounts = {n["nutrient"]["id"]: n.get("amount") for n in food.get("foodNutrients", []) if "nutrient" in n}
                kcal = next((amounts[i] for i in ENERGY_KCAL if amounts.get(i) is not None), None)
                if kcal is None:
                    continue
                per100 = (kcal, amounts.get(PROTEIN) or 0, amounts.get(CARBS) or 0, amounts.get(FAT) or 0)

                # A household portion ("1 medium (118 g)") is easier to use than 100 g. The
                # everyday sizes come first: one egg rather than a cup of eggs.
                portions = []
                for portion in food.get("foodPortions", []):
                    weight = portion.get("gramWeight") or 0
                    unit = (portion.get("modifier") or portion.get("measureUnit", {}).get("name") or "").strip()
                    if weight > 0 and unit and unit != "undetermined":
                        portions.append((portion.get("amount") or 1, unit, weight))
                serving, grams = "100 g", 100.0
                if portions:
                    def rank(p):
                        words = ("medium", "large", "serving", "slice", "piece", "cup")
                        return next((i for i, w in enumerate(words) if w in p[1].lower()), len(words))
                    amount, unit, weight = min(portions, key=rank)
                    serving, grams = f"{amount:g} {unit} ({weight:g} g)", weight

                if row(writer, "usda", str(food["fdcId"]), food["description"].strip(), "", serving, per100, grams):
                    written += 1
    print(f"usda {path}: {written} foods", file=sys.stderr)


# ---------- Open Food Facts (tab-separated, gzip) ----------

def number(text):
    try:
        return float(text)
    except (TypeError, ValueError):
        return None


# ---------- Indian Nutrient Databank (INDB), recipes eaten in India ----------

# INDB counts all the frying oil in a recipe as eaten, so its deep-fried dishes come out far too
# rich: a poori at 738 kcal and 78 g of fat per 100 g, when a real one has about 400 to 500 kcal
# and 25 g. Dishes above either limit are left out: better a missing dish than a wrong number in
# someone's diary. Nothing real is lost for a normal dish (dal, sabzi, roti, rice, idli, dosa).
INDB_MAX_KCAL_PER_100G = 600
INDB_MAX_FAT_PER_100G = 40


def indb(path, writer):
    """INDB (Vijayakumar et al., 2024, CC BY 4.0): about 1,000 Indian dishes such as dal tadka,
    idli or aloo gobi. Each has its nutrients per 100 g and per serving ("1 bowl")."""
    table = xlsx_rows(path)
    header = next(table)
    col = {name: i for i, name in enumerate(header)}
    need = ["food_code", "food_name", "energy_kcal", "protein_g", "carb_g", "fat_g", "servings_unit",
            "unit_serving_energy_kcal", "unit_serving_protein_g", "unit_serving_carb_g", "unit_serving_fat_g"]
    missing = [n for n in need if n not in col]
    if missing:
        sys.exit(f"INDB file is missing columns: {missing}")

    def cell(fields, name):
        i = col[name]
        return fields[i] if i < len(fields) else None

    written, read, skipped_rich = 0, 0, 0
    for fields in table:
        read += 1
        code = (cell(fields, "food_code") or "").strip()
        name = " ".join((cell(fields, "food_name") or "").split())
        per100 = [number(cell(fields, c)) for c in ("energy_kcal", "protein_g", "carb_g", "fat_g")]
        if not code or not name or any(v is None for v in per100):
            continue
        if per100[0] > INDB_MAX_KCAL_PER_100G or per100[3] > INDB_MAX_FAT_PER_100G:
            skipped_rich += 1
            continue
        serving, grams = "100 g", 100.0
        unit = " ".join((cell(fields, "servings_unit") or "").split())
        size = serving_grams(per100, [number(cell(fields, c)) for c in (
            "unit_serving_energy_kcal", "unit_serving_protein_g", "unit_serving_carb_g", "unit_serving_fat_g")])
        # One serving of a dish never has more than ~1,500 kcal. A bigger number means the "serving"
        # in the file is really a whole pot, so the safer 100 g is used instead.
        if unit and size and per100[0] * size / 100 <= 1500:
            serving, grams = f"1 {unit} ({round(size)} g)", size
        if row(writer, "indb", code, name, "", serving, per100, grams):
            written += 1
    print(f"indb {path}: {written} dishes from {read} rows ({skipped_rich} left out as deep-fried or too rich)", file=sys.stderr)


def serving_grams(per100, per_serving):
    """How heavy one serving is, worked out from the two sets of numbers (the file does not
    say). A serving of 210 g has 2.1 times the nutrients of 100 g. Null when the numbers do not
    agree, or the dish has too little of everything to tell."""
    if any(v is None for v in per_serving):
        return None
    ratios = [s / p for p, s in zip(per100, per_serving) if p >= 3]  # a near-zero value says nothing
    if not ratios:
        return None
    low, high = min(ratios), max(ratios)
    if low <= 0 or high / low > 1.15:  # nutrients scaled by different amounts: do not guess
        return None
    grams = sum(ratios) / len(ratios) * 100
    return grams if 5 <= grams <= 1500 else None


def is_indian(code, countries_tags):
    """A product for Indian shoppers: an Indian barcode (GS1 India starts with 890), or one the
    Open Food Facts community tagged as sold in India (imported brands carry foreign codes)."""
    return code.startswith("890") or "en:india" in {t.strip() for t in countries_tags.split(",")}


def off(path, writer, india_only=False):
    csv.field_size_limit(sys.maxsize)
    seen, written, read = set(), 0, 0
    with gzip.open(path, "rt", encoding="utf-8", errors="replace", newline="") as handle:
        # A few rows hold stray NUL bytes, which the csv module refuses to read.
        lines = (line.replace("\0", "") for line in handle)
        reader = csv.reader(lines, delimiter="\t", quoting=csv.QUOTE_NONE)
        header = next(reader)
        col = {name: i for i, name in enumerate(header)}
        need = ["code", "product_name", "brands", "serving_size", "serving_quantity",
                "energy-kcal_100g", "proteins_100g", "carbohydrates_100g", "fat_100g"]
        if india_only:
            need.append("countries_tags")
        missing = [n for n in need if n not in col]
        if missing:
            sys.exit(f"Open Food Facts file is missing columns: {missing}")

        for fields in reader:
            read += 1
            if len(fields) < len(header):
                continue
            code = fields[col["code"]].strip()
            name = " ".join(fields[col["product_name"]].split())
            if not code or not name or code in seen:
                continue
            if india_only and not is_indian(code, fields[col["countries_tags"]]):
                continue
            values = [number(fields[col[c]]) for c in ("energy-kcal_100g", "proteins_100g", "carbohydrates_100g", "fat_100g")]
            if any(v is None for v in values):
                continue
            brand = fields[col["brands"]].split(",")[0].strip()

            serving, grams = "100 g", 100.0
            quantity = number(fields[col["serving_quantity"]])
            label = " ".join(fields[col["serving_size"]].split())
            if quantity and 1 <= quantity <= 2000 and label:
                serving, grams = label, quantity

            if row(writer, "off", code, name, brand, serving, values, grams):
                seen.add(code)
                written += 1
            if read % 500_000 == 0:
                print(f"off: read {read:,}, kept {written:,}", file=sys.stderr)
    print(f"off {path}: {written} products from {read} rows", file=sys.stderr)


def main():
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--usda", nargs="*", default=[], help="FoodData Central JSON zip files")
    parser.add_argument("--indb", help="Indian Nutrient Databank INDB.xlsx (Indian dishes)")
    parser.add_argument("--off", help="Open Food Facts CSV export (.csv.gz)")
    parser.add_argument("--india-only", action="store_true",
                        help="keep only Open Food Facts products with an Indian barcode (890...) or tagged as sold in India")
    args = parser.parse_args()

    writer = csv.writer(sys.stdout)
    for path in args.usda:
        usda(path, writer)
    if args.indb:
        indb(args.indb, writer)
    if args.off:
        off(args.off, writer, india_only=args.india_only)


if __name__ == "__main__":
    main()
