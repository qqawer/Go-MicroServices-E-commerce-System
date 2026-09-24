"""Validate generated SQL and expectations without starting a service/database."""
import csv
import hashlib
import json
from pathlib import Path
import tempfile
import unittest
from urllib.parse import parse_qs, urlsplit

from fixtures import generate


def read_rows(path):
    """Read actual emitted INSERT rows rather than reusing generator helpers."""
    table = None
    columns = None
    with Path(path).open(encoding="utf-8") as handle:
        for line in handle:
            line = line.strip()
            if line.startswith("INSERT INTO "):
                table = line.split()[2]
            elif line.endswith(") VALUES"):
                columns = line[1:-8].split(",")
            elif line.startswith("("):
                values = next(csv.reader([line[1:-2]], quotechar="'", doublequote=True))
                if len(columns) != len(values):
                    raise AssertionError("generated SQL row does not match its columns")
                yield table, dict(zip(columns, values))


class FixturesTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.temporary = tempfile.TemporaryDirectory(prefix="grpctest-benchmark-fixture-")
        cls.directory = Path(cls.temporary.name)
        cls.manifest = generate(10_000, cls.directory / "ten")
        cls.tables = {}
        for table, row in read_rows(cls.directory / "ten" / "fixture.sql"):
            cls.tables.setdefault(table.rsplit(".", 1)[1], []).append(row)

    @classmethod
    def tearDownClass(cls):
        cls.temporary.cleanup()

    def test_deterministic_files_and_size_rejection(self):
        repeated = generate(10_000, self.directory / "repeat")
        self.assertEqual(self.manifest, repeated)
        self.assertEqual((self.directory / "ten" / "fixture.sql").read_bytes(),
                         (self.directory / "repeat" / "fixture.sql").read_bytes())
        sql = (self.directory / "ten" / "fixture.sql").read_bytes()
        self.assertEqual(hashlib.sha256(sql).hexdigest(), self.manifest["sql_sha256"])
        self.assertEqual(json.loads((self.directory / "ten" / "manifest.json").read_text()), self.manifest)
        for size in (0, 9999, 10_001, True):
            with self.assertRaises(ValueError):
                generate(size, self.directory / "rejected")
        with self.assertRaises(ValueError):
            generate(10_000, self.directory / "rejected", seed="20260924")

    def test_required_fields_and_foreign_keys(self):
        self.assertEqual({table: len(rows) for table, rows in self.tables.items()},
                         {"category": 62, "brands": 20, "goods_category_brand": 1000,
                          "goods": 10_000, "inventory": 10_000})
        categories = {int(row["id"]): row for row in self.tables["category"]}
        self.assertEqual(sum(row["level"] == "1" for row in categories.values()), 2)
        self.assertEqual(sum(row["level"] == "2" for row in categories.values()), 10)
        self.assertEqual(sum(row["level"] == "3" for row in categories.values()), 50)
        seen = set()
        for row in self.tables["category"]:
            if row["level"] == "1":
                self.assertEqual(row["parent_category_id"], "NULL")
            else:
                parent = int(row["parent_category_id"])
                self.assertIn(parent, seen)
                self.assertEqual(int(categories[parent]["level"]) + 1, int(row["level"]))
            seen.add(int(row["id"]))
            self.assertTrue(row["url"])
        brands = {int(row["id"]): row for row in self.tables["brands"]}
        self.assertEqual(len({row["name"] for row in brands.values()}), 20)
        relations = {(int(row["category_id"]), int(row["brands_id"]))
                     for row in self.tables["goods_category_brand"]}
        self.assertEqual(len(relations), 1000)
        identifiers = set()
        serials = set()
        for row in self.tables["goods"]:
            identifiers.add(int(row["id"]))
            serials.add(row["goods_sn"])
            leaf, brand = int(row["category_id"]), int(row["brands_id"])
            self.assertIn((leaf, brand), relations)
            self.assertEqual(categories[leaf]["level"], "3")
            self.assertIn(brand, brands)
            self.assertEqual((row["inventory_ready"], row["on_sale"], row["stocks"]), ("1", "1", "100"))
            self.assertTrue(1 <= int(row["shop_price"]) <= 2000)
            self.assertEqual(int(row["market_price"]), int(row["shop_price"]) + 100)
            self.assertEqual(json.loads(row["images"]), [row["goods_front_image"]])
            self.assertEqual(json.loads(row["desc_images"]), [])
        self.assertEqual(identifiers, set(range(1, 10_001)))
        self.assertEqual(len(serials), 10_000)
        inventory_goods = {int(row["goods"]) for row in self.tables["inventory"]}
        self.assertEqual(inventory_goods, identifiers)
        self.assertTrue(all(row["stocks"] == "100" for row in self.tables["inventory"]))
        # A brand has many distinct prices and categories, not one correlated bin.
        brand_one = [row for row in self.tables["goods"] if row["brands_id"] == "1"]
        self.assertGreater(len({row["shop_price"] for row in brand_one}), 100)
        self.assertGreater(len({row["category_id"] for row in brand_one}), 40)

    def test_manifest_counts_are_independently_computed_from_sql(self):
        goods = self.tables["goods"]
        cases = self.manifest["cases"]
        first_root_children = {row["id"] for row in self.tables["category"]
                               if row["parent_category_id"] == "1"}
        first_root_leaves = {row["id"] for row in self.tables["category"]
                             if row["parent_category_id"] in first_root_children}
        expected = {
            "list": len(goods), "deep": len(goods),
            "filtered": sum(row["brands_id"] == "1" and 100 <= int(row["shop_price"]) <= 1000 for row in goods),
            "price": sum(100 <= int(row["shop_price"]) <= 1000 for row in goods),
            "category": sum(row["category_id"] in first_root_leaves for row in goods),
            "keyword": sum("Focus" in row["name"] for row in goods),
        }
        self.assertEqual({name: case["total"] for name, case in cases.items()}, expected)
        self.assertEqual(set(cases["category"]["predicate"]["category_ids"]), set(map(int, first_root_leaves)))
        self.assertEqual(expected["keyword"], 1000)
        for name, case in cases.items():
            parsed = urlsplit(case["path"])
            query = parse_qs(parsed.query)
            self.assertEqual(parsed.path, "/g/v1/goods")
            self.assertEqual(int(query["p"][0]), case["page"])
            self.assertEqual(int(query["pnum"][0]), case["page_size"])
            self.assertEqual(case["page_size"], 20)
            self.assertGreaterEqual(case["total"] - (case["page"] - 1) * 20, 20)
            predicate = case["predicate"]
            for key, parameter in (("brand", "b"), ("min_price", "pmin"), ("max_price", "pmax")):
                if key in predicate:
                    self.assertEqual(int(query[parameter][0]), predicate[key])
            if name == "keyword":
                self.assertEqual(query["q"][0], predicate["keyword"])
        by_id = {int(row["id"]): row for row in goods}
        brands = {int(row["id"]): row for row in self.tables["brands"]}
        categories = {int(row["id"]): row for row in self.tables["category"]}
        samples = self.manifest["details"]
        self.assertEqual(len(samples), 1024)
        self.assertEqual(len({row["id"] for row in samples}), 1024)
        self.assertLess(min(row["id"] for row in samples), 100)
        self.assertGreater(max(row["id"] for row in samples), 9900)
        for sample in samples:
            row = by_id[sample["id"]]
            self.assertEqual(sample["name"], row["name"])
            self.assertEqual(sample["shop_price"], int(row["shop_price"]))
            self.assertEqual(sample["brand"]["id"], int(row["brands_id"]))
            self.assertEqual(sample["brand"]["name"], brands[int(row["brands_id"])]["name"])
            self.assertEqual(sample["ctegory"], {"id": int(row["category_id"]),
                                                "name": categories[int(row["category_id"])]["name"]})

    def test_hundred_thousand_rows_and_batch_size(self):
        manifest = generate(100_000, self.directory / "hundred")
        counts = {}
        first_ten_hash = hashlib.sha256()
        ten_hash = hashlib.sha256()
        for table, row in read_rows(self.directory / "hundred" / "fixture.sql"):
            counts[table] = counts.get(table, 0) + 1
            if table == "grpctest_goods_srv.goods" and int(row["id"]) <= 10_000:
                first_ten_hash.update(json.dumps(row, sort_keys=True).encode())
        for row in self.tables["goods"]:
            ten_hash.update(json.dumps(row, sort_keys=True).encode())
        self.assertEqual(first_ten_hash.hexdigest(), ten_hash.hexdigest())
        self.assertEqual(counts["grpctest_goods_srv.goods"], 100_000)
        self.assertEqual(counts["grpctest_inventory_srv.inventory"], 100_000)
        self.assertEqual(manifest["cases"]["keyword"]["total"], 10_000)
        self.assertEqual(manifest["cases"]["deep"]["page"], 4000)
        batch = 0
        for line in (self.directory / "hundred" / "fixture.sql").read_text().splitlines():
            if line.startswith("(") and not line.endswith(") VALUES"):
                batch += 1
            if line.endswith(";"):
                self.assertGreater(batch, 0)
                self.assertLessEqual(batch, 500)
                batch = 0


if __name__ == "__main__":
    unittest.main()
