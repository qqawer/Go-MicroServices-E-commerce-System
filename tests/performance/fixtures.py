#!/usr/bin/env python3
"""Generate deterministic, synthetic goods fixtures for an owned test database.

This module does not connect to any database. Apply fixture.sql to a fresh
tests.env.Environment with env.dbtool('seed', seed=path), after migrations.
"""
import argparse
import hashlib
import json
from pathlib import Path
import random

DEFAULT_SEED = 20260924
SIZES = (10_000, 100_000)
BATCH_SIZE = 500
PAGE_SIZE = 20
DETAIL_SAMPLES = 1024
KEYWORD = "Focus"
STAMP = "2026-01-01 00:00:00"


def _literal(value):
    if value is None:
        return "NULL"
    if isinstance(value, int):
        return str(value)
    return "'" + str(value).replace("\\", "\\\\").replace("'", "''") + "'"


def _insert(handle, table, columns, rows):
    """Keep each statement small while retaining dbtool's single transaction."""
    batch = []

    def flush():
        handle.write("INSERT INTO " + table + "\n(" + ",".join(columns) + ") VALUES\n")
        handle.write(",\n".join("(" + ",".join(map(_literal, row)) + ")" for row in batch))
        handle.write(";\n")

    for row in rows:
        batch.append(row)
        if len(batch) == BATCH_SIZE:
            flush()
            batch.clear()
    if batch:
        flush()


def _categories():
    rows = []
    for root in range(1, 3):
        rows.append((root, f"Benchmark root {root}", None, 1))
        for branch in range(5):
            middle = 10 + (root - 1) * 5 + branch
            rows.append((middle, f"Benchmark category {middle}", root, 2))
            for leaf in range(5):
                identifier = 100 + (middle - 10) * 5 + leaf
                rows.append((identifier, f"Benchmark leaf {identifier}", middle, 3))
    # Parent rows precede their children, so normal FK enforcement stays enabled.
    return rows


def _products(size, seed):
    # Independent streams prevent a product's brand/category from determining
    # its price. The 10k dataset is also an exact prefix of the 100k dataset.
    brands = random.Random(seed ^ 0xB12A)
    categories = random.Random(seed ^ 0xCA7E)
    prices = random.Random(seed ^ 0xA11C)
    for identifier in range(1, size + 1):
        yield {
            "id": identifier,
            "name": f"Benchmark {KEYWORD if identifier % 10 == 0 else 'Standard'} goods {identifier:06d}",
            "brand_id": brands.randint(1, 20),
            "category_id": categories.randint(100, 149),
            "shop_price": prices.randint(1, 2000),
        }


def generate(size, out_dir, seed=DEFAULT_SEED):
    """Write fixture.sql and manifest.json; return the public manifest dict."""
    if isinstance(size, bool) or size not in SIZES:
        raise ValueError("size must be 10000 or 100000")
    if not isinstance(seed, int) or isinstance(seed, bool):
        raise ValueError("seed must be an integer")
    directory = Path(out_dir)
    directory.mkdir(parents=True, exist_ok=True)
    sql_path = directory / "fixture.sql"
    categories = _categories()
    category_by_id = {row[0]: row for row in categories}
    brands = {
        identifier: {
            "id": identifier,
            "name": f"Benchmark brand {identifier:02d}",
            "logo": f"https://example.invalid/benchmark/brand-{identifier:02d}.png",
        }
        for identifier in range(1, 21)
    }
    sampled_ids = random.Random(seed ^ 0xDE7A).sample(range(1, size + 1), DETAIL_SAMPLES)
    sample_set = set(sampled_ids)
    sampled = {}
    totals = {name: 0 for name in ("filtered", "price", "category", "keyword")}

    def product_rows():
        for item in _products(size, seed):
            identifier = item["id"]
            price = item["shop_price"]
            leaf = item["category_id"]
            brand = item["brand_id"]
            in_range = 100 <= price <= 1000
            totals["filtered"] += int(brand == 1 and in_range)
            totals["price"] += int(in_range)
            totals["category"] += int(100 <= leaf <= 124)
            totals["keyword"] += int(KEYWORD in item["name"])
            if identifier in sample_set:
                sampled[identifier] = {
                    "id": identifier,
                    "name": item["name"],
                    "shop_price": price,
                    "brand": brands[brand],
                    # The public HTTP response currently spells this ctegory.
                    "ctegory": {"id": leaf, "name": category_by_id[leaf][1]},
                }
            image = f"https://example.invalid/benchmark/goods-{identifier:06d}.png"
            yield (
                identifier, STAMP, STAMP, 0, leaf, brand, 1,
                f"BENCH-{identifier:06d}", item["name"], identifier % 101,
                identifier % 53, identifier % 17, 100, price + 100, price,
                "Synthetic performance fixture", int(identifier % 2 == 0),
                json.dumps([image], separators=(",", ":")), "[]", image,
                int(identifier % 5 == 0), int(identifier % 7 == 0), 1,
            )

    with sql_path.open("w", encoding="utf-8", newline="\n") as handle:
        handle.write(f"-- Synthetic benchmark fixtures: size={size}, seed={seed}.\n")
        _insert(handle, "grpctest_goods_srv.category",
                ("id", "name", "parent_category_id", "level", "is_tab", "url", "add_time", "update_time", "is_deleted"),
                ((identifier, name, parent, level, 0,
                  f"https://example.invalid/benchmark/category-{identifier}", STAMP, STAMP, 0)
                 for identifier, name, parent, level in categories))
        _insert(handle, "grpctest_goods_srv.brands",
                ("id", "name", "logo", "add_time", "update_time", "is_deleted"),
                ((item["id"], item["name"], item["logo"], STAMP, STAMP, 0) for item in brands.values()))
        _insert(handle, "grpctest_goods_srv.goods_category_brand",
                ("id", "category_id", "brands_id", "add_time", "update_time", "is_deleted"),
                (((leaf - 100) * 20 + brand, leaf, brand, STAMP, STAMP, 0)
                 for leaf in range(100, 150) for brand in range(1, 21)))
        _insert(handle, "grpctest_goods_srv.goods",
                ("id", "add_time", "update_time", "is_deleted", "category_id", "brands_id", "on_sale",
                 "goods_sn", "name", "click_num", "sold_num", "fav_num", "stocks", "market_price",
                 "shop_price", "goods_brief", "ship_free", "images", "desc_images", "goods_front_image",
                 "is_new", "is_hot", "inventory_ready"), product_rows())
        _insert(handle, "grpctest_inventory_srv.inventory",
                ("id", "add_time", "update_time", "is_deleted", "goods", "stocks", "version"),
                ((identifier, STAMP, STAMP, 0, identifier, 100, 0) for identifier in range(1, size + 1)))

    def case(path, total, predicate=None, page=1):
        return {"path": path, "total": total, "page_size": PAGE_SIZE,
                "page": page, "predicate": predicate or {}}

    deep_page = (size // PAGE_SIZE) * 4 // 5
    manifest = {
        "size": size,
        "seed": seed,
        "sql_sha256": hashlib.sha256(sql_path.read_bytes()).hexdigest(),
        "counts": {"goods": size, "inventory": size, "brands": 20,
                   "categories": len(categories), "category_brands": 1000},
        "details": [sampled[identifier] for identifier in sampled_ids],
        "cases": {
            "list": case("/g/v1/goods?p=1&pnum=20", size),
            "deep": case(f"/g/v1/goods?p={deep_page}&pnum=20", size, page=deep_page),
            "filtered": case("/g/v1/goods?b=1&pmin=100&pmax=1000&p=1&pnum=20", totals["filtered"],
                             {"brand": 1, "min_price": 100, "max_price": 1000}),
            "price": case("/g/v1/goods?pmin=100&pmax=1000&p=1&pnum=20", totals["price"],
                          {"min_price": 100, "max_price": 1000}),
            "category": case("/g/v1/goods?c=1&p=1&pnum=20", totals["category"],
                             {"category_ids": list(range(100, 125))}),
            "keyword": case(f"/g/v1/goods?q={KEYWORD}&p=1&pnum=20", totals["keyword"],
                            {"keyword": KEYWORD}),
        },
    }
    (directory / "manifest.json").write_text(json.dumps(manifest, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    return manifest


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--size", type=int, choices=SIZES, required=True)
    parser.add_argument("--out-dir", type=Path, required=True)
    parser.add_argument("--seed", type=int, default=DEFAULT_SEED)
    args = parser.parse_args()
    manifest = generate(args.size, args.out_dir, args.seed)
    print(json.dumps({"size": manifest["size"], "seed": manifest["seed"],
                      "sql_sha256": manifest["sql_sha256"], "out_dir": str(args.out_dir)}))


if __name__ == "__main__":
    main()
