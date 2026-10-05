#!/usr/bin/env python3
"""Restore the repository goods backup into a NEW local lab database only."""
import hashlib
import json
from pathlib import Path
import re
import subprocess

ROOT = Path(__file__).resolve().parents[2]
BACKUP = ROOT / "goods_srv/grpctest_goods_srv.sql"
DATABASE = "es_goods_snapshot_20261005"
MYSQL = ["docker", "exec", "-i", "grpctest-es-lab-mysql-1", "mysql",
         "--default-character-set=utf8mb4", "-uroot", "--batch", "--skip-column-names"]


def sql(query):
    return subprocess.run(MYSQL, input=query, text=True, capture_output=True, check=True).stdout.strip()


if __name__ == "__main__":
    exists = sql(f"SELECT COUNT(*) FROM information_schema.schemata WHERE schema_name='{DATABASE}';")
    if exists != "0":
        raise SystemExit(f"Refusing to overwrite existing database {DATABASE}; rerun only the Go importer.")
    source = BACKUP.read_bytes()
    digest = hashlib.sha256(source).hexdigest()
    # The reviewed repository dump uses unqualified table names and has no USE
    # or CREATE DATABASE statements. Refuse a changed dump that could switch DBs.
    statements = re.sub(r"\A/\*.*?\*/", "", source.decode(), count=1, flags=re.S)
    if re.search(r"(?im)^\s*(USE\s|CREATE\s+DATABASE|DROP\s+DATABASE|GRANT\s|CREATE\s+USER|SOURCE\s)", statements):
        raise SystemExit("Dump contains a database/account statement; review it before restoring.")
    sql(f"CREATE DATABASE {DATABASE} CHARACTER SET utf8mb4 COLLATE utf8mb4_general_ci;\n"
        f"USE {DATABASE};\n" + source.decode() + "\n"
        # The old backup predates inventory_ready. NULL means unknown, not ready.
        "ALTER TABLE goods ADD COLUMN inventory_ready BOOLEAN NULL DEFAULT NULL;")
    counts = sql(f"SELECT COUNT(*), SUM(deleted_at IS NULL), SUM(inventory_ready IS NULL) FROM {DATABASE}.goods;")
    report = {
        "source": str(BACKUP.relative_to(ROOT)), "sha256": digest,
        "database": DATABASE, "total_active_unknown_inventory": counts.split("\t"),
        "inventory_ready": "NULL: absent from the historical backup; not inferred from stocks",
    }
    output = ROOT / ".local/es-lab/goods-import"
    output.mkdir(parents=True, exist_ok=True)
    (output / "source.json").write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n")
    print(json.dumps(report, ensure_ascii=False, indent=2))
