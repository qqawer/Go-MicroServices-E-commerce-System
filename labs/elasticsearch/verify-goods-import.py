#!/usr/bin/env python3
"""Read-only comparison of every imported field against the local snapshot."""
import argparse
from decimal import Decimal, ROUND_HALF_UP
import json
from pathlib import Path
import subprocess
import urllib.request

parser = argparse.ArgumentParser()
parser.add_argument("--index", default="grpctest-goods-snapshot-20261005")
parser.add_argument("--limit", type=int, default=0)
args = parser.parse_args()
assert args.limit >= 0
assert args.index.startswith("grpctest-goods-snapshot-") and "/" not in args.index

query = """SELECT JSON_OBJECT(
    'id', id, 'name', name, 'brief', goods_brief,
    'category_id', category_id, 'brand_id', brands_id,
    'price_yuan', CAST(shop_price AS CHAR),
    'on_sale', on_sale, 'inventory_ready', inventory_ready,
    'ship_free', ship_free, 'is_new', is_new, 'is_hot', is_hot)
    FROM goods WHERE deleted_at IS NULL ORDER BY id"""
if args.limit:
    query += f" LIMIT {args.limit}"
raw = subprocess.check_output([
    "docker", "exec", "grpctest-es-lab-mysql-1", "mysql",
    "-uroot", "--default-character-set=utf8mb4", "--batch", "--raw", "--skip-column-names",
    "es_goods_snapshot_20261005", "-e", query,
], text=True)
expected = [json.loads(line) for line in raw.splitlines()]
for doc in expected:
    # Independently check the SQL conversion used by the Go importer.
    price = Decimal(doc.pop("price_yuan")).quantize(Decimal("0.01"), rounding=ROUND_HALF_UP)
    doc["price_cents"] = int(price * 100)
    for key in ["on_sale", "inventory_ready", "ship_free", "is_new", "is_hot"]:
        if doc[key] is not None:
            doc[key] = bool(doc[key])


def search(body):
    req = urllib.request.Request(
        f"http://127.0.0.1:9201/{args.index}/_search", method="POST",
        data=json.dumps(body).encode(), headers={"Content-Type": "application/json"},
    )
    with urllib.request.urlopen(req, timeout=15) as response:
        result = json.load(response)
    assert not result["timed_out"] and result["_shards"]["failed"] == 0, result
    return result["hits"]


# The historical source has only 420 documents, within the default result window.
hits = search({"size": len(expected), "track_total_hits": True, "sort": [{"id": "asc"}]})
assert hits["total"]["relation"] == "eq" and hits["total"]["value"] == len(expected), hits["total"]
assert len(hits["hits"]) == len(expected)
for source, hit in zip(expected, hits["hits"]):
    assert hit["_id"] == str(source["id"]), hit["_id"]
    assert hit["_source"] == source, f"field mismatch for goods {source['id']}"

apple = search({"track_total_hits": True, "query": {"match": {"name": "苹果"}}, "size": 5})
report = {
    "index": args.index, "documents_compared": len(expected), "all_fields_match": True,
    "apple_name_matches": apple["total"]["value"],
    "apple_examples": [{"id": h["_id"], "name": h["_source"]["name"],
                        "price_cents": h["_source"]["price_cents"]} for h in apple["hits"]],
}
output = Path(__file__).resolve().parents[2] / ".local/es-lab/goods-import"
output.mkdir(parents=True, exist_ok=True)
(output / f"{args.index}-verification.json").write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n")
print(json.dumps(report, ensure_ascii=False, indent=2))
