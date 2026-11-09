"""Run with: python3 scripts/test_import_foods.py"""
import csv
import io
import os
import tempfile
import unittest
import zipfile

from import_foods import INDB_MAX_FAT_PER_100G, INDB_MAX_KCAL_PER_100G, indb, is_indian, serving_grams


class IsIndian(unittest.TestCase):
    def test_indian_barcode(self):
        self.assertTrue(is_indian("8901030865275", ""))
        self.assertTrue(is_indian("8901030865275", "en:france"))

    def test_sold_in_india_with_a_foreign_barcode(self):
        self.assertTrue(is_indian("5449000000996", "en:india,en:united-kingdom"))
        self.assertTrue(is_indian("5449000000996", " en:india "))

    def test_everything_else(self):
        self.assertFalse(is_indian("3017620422003", "en:france,en:germany"))
        self.assertFalse(is_indian("3017620422003", ""))
        self.assertFalse(is_indian("5449000000996", "en:indiana"), "must match the whole tag")
        self.assertFalse(is_indian("0890123456789", ""), "890 must be the start of the code")


class ServingGrams(unittest.TestCase):
    def test_worked_out_from_the_ratio(self):
        # 210 g of a dish with 100 kcal, 5 protein, 20 carbs, 2 fat per 100 g.
        self.assertAlmostEqual(serving_grams([100, 5, 20, 2], [210, 10.5, 42, 4.2]), 210, places=3)

    def test_nutrients_that_do_not_agree_give_nothing(self):
        self.assertIsNone(serving_grams([100, 5, 20, 2], [210, 10.5, 90, 4.2]))

    def test_missing_or_tiny_values_give_nothing(self):
        self.assertIsNone(serving_grams([100, 5, 20, 2], [210, None, 42, 4.2]))
        self.assertIsNone(serving_grams([0, 0, 0, 0], [0, 0, 0, 0]), "water: nothing to measure by")

    def test_a_silly_weight_gives_nothing(self):
        self.assertIsNone(serving_grams([100, 5, 20, 2], [2, 0.1, 0.4, 0.04]), "2 g")
        self.assertIsNone(serving_grams([100, 5, 20, 2], [5000, 250, 1000, 100]), "5 kg")


def make_xlsx(path, rows):
    """A minimal .xlsx with one sheet, to test the reader and the import together."""
    strings, cells = [], []
    for r, row in enumerate(rows, start=1):
        for c, value in enumerate(row):
            ref = f"{chr(65 + c)}{r}"
            if value is None:
                continue
            if isinstance(value, str):
                strings.append(value)
                cells.append(f'<c r="{ref}" t="s"><v>{len(strings) - 1}</v></c>')
            else:
                cells.append(f'<c r="{ref}"><v>{value}</v></c>')
    by_row = {}
    for cell in cells:
        by_row.setdefault(int("".join(ch for ch in cell.split('r="')[1].split('"')[0] if ch.isdigit())), []).append(cell)
    sheet = "".join(f'<row r="{n}">{"".join(cs)}</row>' for n, cs in sorted(by_row.items()))
    ns = 'xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"'
    with zipfile.ZipFile(path, "w") as z:
        z.writestr("xl/sharedStrings.xml", f'<sst {ns}>' + "".join(f"<si><t>{s}</t></si>" for s in strings) + "</sst>")
        z.writestr("xl/worksheets/sheet1.xml", f'<worksheet {ns}><sheetData>{sheet}</sheetData></worksheet>')


class Indb(unittest.TestCase):
    HEADER = ["food_code", "food_name", "energy_kcal", "protein_g", "carb_g", "fat_g", "servings_unit",
              "unit_serving_energy_kcal", "unit_serving_protein_g", "unit_serving_carb_g", "unit_serving_fat_g"]

    def convert(self, rows):
        with tempfile.TemporaryDirectory() as tmp:
            path = os.path.join(tmp, "INDB.xlsx")
            make_xlsx(path, [self.HEADER] + rows)
            out = io.StringIO()
            indb(path, csv.writer(out))
        return list(csv.reader(io.StringIO(out.getvalue())))

    def test_a_dish_with_a_serving(self):
        got = self.convert([["ASC144", "Idli", 136, 4.8, 28.4, 0.4, "idli", 34, 1.2, 7.1, 0.1]])
        self.assertEqual(got, [["indb", "ASC144", "Idli", "", "1 idli (25 g)", "34", "1.2", "7.1", "0.1"]])

    def test_a_dish_without_a_serving_unit_uses_100_g(self):
        got = self.convert([["BFP001", "Dal", 100, 5.0, 15.0, 2.0, None, None, None, None, None]])
        self.assertEqual(got[0][4:], ["100 g", "100", "5.0", "15.0", "2.0"])

    def test_a_whole_pot_is_not_called_one_serving(self):
        got = self.convert([["ASC367", "Big pot", 300, 5, 40, 12, "pot", 1800, 30, 240, 72]])
        self.assertEqual(got[0][4], "100 g")

    def test_dishes_that_are_too_rich_to_be_real_are_left_out(self):
        rich = INDB_MAX_KCAL_PER_100G + 40
        got = self.convert([
            ["F1", "Poori", rich, 7, 45, 50, None, None, None, None, None],
            ["F2", "Plain dosa", 380, 10, 64, 8, None, None, None, None, None],
        ])
        self.assertEqual([r[1] for r in got], ["F2"])

    def test_deep_fried_dishes_with_an_impossible_amount_of_fat_are_left_out(self):
        got = self.convert([
            ["F3", "Samosa", 520, 5, 30, INDB_MAX_FAT_PER_100G + 19, None, None, None, None, None],
            ["F4", "Paratha", 298, 6, 40, 12, None, None, None, None, None],
        ])
        self.assertEqual([r[1] for r in got], ["F4"])

    def test_impossible_rows_are_dropped(self):
        got = self.convert([
            ["X1", "Too much protein", 100, 150, 10, 1, None, None, None, None, None],
            ["X2", "Missing numbers", None, None, None, None, None, None, None, None, None],
            ["", "No code", 100, 5, 15, 2, None, None, None, None, None],
            ["X3", "Fine", 100, 5, 15, 2, None, None, None, None, None],
        ])
        self.assertEqual([r[1] for r in got], ["X3"])


if __name__ == "__main__":
    unittest.main()
