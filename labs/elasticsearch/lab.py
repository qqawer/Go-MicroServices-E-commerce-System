#!/usr/bin/env python3
"""Small, local-only ES experiments. Run each step separately to inspect results."""
import argparse
import json
from pathlib import Path
import subprocess
import urllib.error
import urllib.request

HERE = Path(__file__).resolve().parent
OUT = HERE.parents[1] / ".local" / "es-lab"
URL = "http://127.0.0.1:9201"
INDEX = "grpctest-es-lab-products"
MARKER = "grpctest-es-stage-1"
TRACE = []
HTTP = urllib.request.build_opener(urllib.request.ProxyHandler({}))


def check(condition, message):
    if not condition:
        raise RuntimeError(message)


def api(method, path, body=None, allowed=(200, 201)):
    raw = isinstance(body, str)
    data = (body if raw else json.dumps(body, ensure_ascii=False)).encode() if body is not None else None
    request = urllib.request.Request(URL + path, data=data, method=method, headers={
        "Content-Type": "application/x-ndjson" if raw else "application/json",
    })
    try:
        response = HTTP.open(request, timeout=30)
    except urllib.error.HTTPError as error:
        response = error
    with response:
        result = json.load(response)
        TRACE.append({"method": method, "path": path, "body": body,
                      "status": response.status, "response": result})
        check(response.status in allowed, f"{method} {path}: {response.status} {result}")
        return result


def mysql(sql):
    result = subprocess.run([
        "docker", "compose", "-p", "grpctest-es-lab", "-f", str(HERE / "compose.yaml"),
        "exec", "-T", "mysql", "mysql", "--default-character-set=utf8mb4",
        "-uroot", "--batch", "--skip-column-names", "es_lab",
    ], input=sql, text=True, capture_output=True, timeout=30, check=True)
    TRACE.append({"sql": sql, "result": result.stdout})
    return result.stdout.strip()


def literal(text):
    return "CONVERT(0x" + text.encode().hex() + " USING utf8mb4)"


def fixtures():
    return [json.loads(line) for line in (HERE / "products.ndjson").read_text().splitlines()]


def search(query):
    result = api("POST", f"/{INDEX}/_search", {
        "query": query, "size": 100, "track_total_hits": True,
        "sort": [{"_score": "desc"}, {"id": "asc"}],
    })
    check(not result["timed_out"] and result["_shards"]["failed"] == 0, "Incomplete search")
    return result["hits"]["hits"]


def ids(hits):
    return [hit["_source"]["id"] for hit in hits]


def bulk(products):
    lines = []
    for product in products:
        lines.extend([json.dumps({"index": {"_id": str(product["id"])}}),
                      json.dumps(product, ensure_ascii=False)])
    return api("POST", f"/{INDEX}/_bulk?refresh=wait_for", "\n".join(lines) + "\n")


def owned_index():
    mapping = api("GET", f"/{INDEX}/_mapping", allowed=(200, 404))
    if INDEX in mapping:
        check(mapping[INDEX]["mappings"].get("_meta", {}).get("lab") == MARKER,
              "Refusing to modify an index without the lab marker")
        return True
    return False


def analyze():
    for text in ["苹果手机", "红富士苹果", "苹果 智能 手机", "手机苹果", "不存在的火星商品"]:
        for analyzer in ["standard", "smartcn"]:
            result = api("POST", "/_analyze", {"analyzer": analyzer, "text": text})
            tokens = [token["token"] for token in result["tokens"]]
            print(f"{analyzer:8} {text}: {' / '.join(tokens)}")
            if analyzer == "smartcn" and text == "苹果手机":
                check(tokens == ["苹果", "手机"], "Unexpected pinned analyzer output")


def seed():
    if owned_index():
        api("DELETE", f"/{INDEX}")
    api("PUT", f"/{INDEX}", json.loads((HERE / "mapping.json").read_text()))
    products = fixtures()
    check(len(products) == 40 and len({p["id"] for p in products}) == 40, "Invalid fixture IDs")
    for attempt in range(2):
        result = bulk(products)
        failures = [item for item in result["items"] if item["index"]["status"] >= 300]
        check(not result["errors"] and not failures and len(result["items"]) == 40,
              f"Bulk failures: {failures}")
        check(api("GET", f"/{INDEX}/_count")["count"] == 40, "Bulk count mismatch")
        print(f"Bulk pass {attempt + 1}: 40 items succeeded; ES count remains 40")
    mysql("""DROP TABLE IF EXISTS products;
CREATE TABLE products (
 id INT PRIMARY KEY, name VARCHAR(255), brief TEXT, category VARCHAR(32),
 brand VARCHAR(32), price_cents INT, on_sale BOOLEAN, inventory_ready BOOLEAN
) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;
""" + "\n".join(
        "INSERT INTO products VALUES (" + ",".join([
            str(p["id"]), *(literal(p[key]) for key in ["name", "brief", "category", "brand"]),
            str(p["price_cents"]), str(int(p["on_sale"])), str(int(p["inventory_ready"])),
        ]) + ");" for p in products))
    check(mysql("SELECT COUNT(*) FROM products;") == "40", "MySQL count mismatch")
    print("MySQL:", mysql("SELECT VERSION(), @@collation_database;"), "; count=40")


def queries():
    comparisons = []
    for text in ["苹果", "苹果手机", "手机苹果", "红富士苹果", "不存在的火星商品", "zzznomatch20261002"]:
        sql = mysql("SELECT id FROM products WHERE name LIKE " + literal("%" + text + "%") + " ORDER BY id;")
        row = {"query": text, "LIKE_name": [int(i) for i in sql.splitlines()]}
        for operator in ["or", "and"]:
            row["match_" + operator] = ids(search({"match": {"name": {"query": text, "operator": operator}}}))
        row["multi_match"] = ids(search({"multi_match": {
            "query": text, "fields": ["name^3", "brief"], "type": "best_fields", "operator": "or",
        }}))
        comparisons.append(row)
        print(json.dumps(row, ensure_ascii=False))
    apple, phone, reversed_words, fuji, broad, empty = comparisons
    check({1, 3}.issubset(apple["match_or"]), "Apple should match phones and fruit")
    check(7 not in apple["match_or"] and 7 in apple["multi_match"], "Brief-only match missing")
    check(2 not in phone["LIKE_name"] and 2 in phone["match_and"], "Token vs substring difference missing")
    check(not reversed_words["LIKE_name"] and 1 in reversed_words["match_and"], "AND should not require term order")
    check(not broad["LIKE_name"] and 32 in broad["match_or"], "OR can match just the shared word 商品")
    check(all(not value for key, value in empty.items() if key != "query"), "Expected empty results")

    fulltext = {"multi_match": {"query": "苹果手机", "fields": ["name^3", "brief"], "operator": "and"}}
    filters = [{"term": {"category": "phone"}}, {"term": {"brand": "apple"}},
               {"range": {"price_cents": {"gte": 500000, "lte": 650000}}},
               {"term": {"on_sale": True}}, {"term": {"inventory_ready": True}}]
    filtered = search({"bool": {"must": [fulltext], "filter": filters}})
    sql = """SELECT id FROM products WHERE name LIKE '%苹果手机%' AND category='phone'
AND brand='apple' AND price_cents BETWEEN 500000 AND 650000
AND on_sale=1 AND inventory_ready=1 ORDER BY id;"""
    check(ids(filtered) == [1] and mysql(sql) == "1", "Combined filter mismatch")
    visible = search({"bool": {"must": [fulltext], "filter": [filters[0], filters[3], filters[4]]}})
    check(set(ids(visible)) == {1, 2}, "Visibility/category filters failed")
    print("Combined category+brand+price+visibility filters:", ids(filtered))
    print("Phone category + visibility only:", ids(visible))

    check(not search({"term": {"name": "苹果手机"}}), "term must not analyze text")
    check(ids(search({"term": {"name.raw": "苹果手机 128G"}})) == [1], "keyword exact match failed")
    for token in ["苹果", "手机", "红富士"]:
        print("name term", token, "→ IDs", sorted(ids(search({"term": {"name": token}}))))

    scores = {}
    for fields in [["name", "brief"], ["name^3", "brief"]]:
        hits = search({"bool": {
            "must": [{"multi_match": {"query": "苹果", "fields": fields, "type": "best_fields"}}],
            "filter": [{"term": {"category": "demo"}}],
        }})
        scores[fields[0]] = {hit["_source"]["id"]: hit["_score"] for hit in hits}
        print("Field weights", fields, "→", [(h["_id"], h["_score"]) for h in hits])
    check(abs(scores["name^3"][31] / scores["name"][31] - 3) < 0.001, "Name boost mismatch")
    check(scores["name^3"][32] == scores["name"][32], "Brief-only score should not get name boost")
    api("POST", f"/{INDEX}/_explain/31", {"query": {"multi_match": {
        "query": "苹果", "fields": ["name^3", "brief"], "type": "best_fields",
    }}})
    print("PASS: search semantics, exact filters, field boosts; full _explain saved in trace")


def writes():
    check(api("GET", f"/{INDEX}/_count")["count"] == 40, "Run seed before writes")
    product = dict(fixtures()[0], id=90, name="临时实验手机")
    query = {"ids": {"values": ["90"]}}
    # Disable automatic refresh briefly to demonstrate visibility without a timing race.
    api("PUT", f"/{INDEX}/_settings", {"index": {"refresh_interval": "-1"}})
    try:
        created = api("PUT", f"/{INDEX}/_doc/90", product)
        check(created["result"] == "created", "Expected a new document")
        check(api("GET", f"/{INDEX}/_doc/90")["found"], "Realtime GET failed")
        check(not search(query), "Document should be hidden before refresh")
        api("POST", f"/{INDEX}/_refresh")
        check(ids(search(query)) == [90], "Refresh did not expose the document")
        print("Create: realtime GET found=true; search 0 → explicit refresh → search 1")
    finally:
        api("PUT", f"/{INDEX}/_settings", {"index": {"refresh_interval": "1s"}})
    api("POST", f"/{INDEX}/_update/90?refresh=wait_for", {"doc": {"price_cents": 12345}})
    check(search(query)[0]["_source"]["price_cents"] == 12345, "Update not searchable")
    api("DELETE", f"/{INDEX}/_doc/90?refresh=wait_for")
    check(not search(query), "Delete not reflected in search")
    print("Update price to 12345 cents verified; delete verified")

    result = bulk([dict(product, id=91), dict(product, id=92, price_cents="invalid")])
    statuses = [(item["index"]["_id"], item["index"]["status"]) for item in result["items"]]
    check(result["errors"] and statuses == [("91", 201), ("92", 400)], "Unexpected partial bulk result")
    check(ids(search({"ids": {"values": ["91", "92"]}})) == [91], "Partial bulk state mismatch")
    api("DELETE", f"/{INDEX}/_doc/91?refresh=wait_for")
    check(api("GET", f"/{INDEX}/_count")["count"] == 40, "Temporary documents remain")
    print("Bulk HTTP 200, errors=true:", statuses, "; temporary documents cleaned; count=40")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("step", choices=["analyze", "seed", "search", "writes", "all"])
    step = parser.parse_args().step
    OUT.mkdir(parents=True, exist_ok=True)
    try:
        info = api("GET", "/")
        check(info["cluster_name"] == "grpctest-es-lab" and info["version"]["number"] == "8.19.22",
              "Expected the pinned local lab cluster")
        if step in ["search", "writes"]:
            check(owned_index(), "Run seed first")
        steps = {"analyze": analyze, "seed": seed, "search": queries, "writes": writes}
        for name in steps if step == "all" else [step]:
            steps[name]()
    finally:
        output = OUT / (step + ".json")
        output.write_text(json.dumps(TRACE, ensure_ascii=False, indent=2) + "\n")
        print("HTTP/SQL trace:", output)


if __name__ == "__main__":
    main()
