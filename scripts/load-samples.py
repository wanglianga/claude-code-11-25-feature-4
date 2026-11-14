#!/usr/bin/env python3
"""把 samples/ 下的样例按真实 id 注入到运行中的平台。

样例文件里写的是人类可读的占位（如 "TRIP-1 批次 id"），本脚本在运行时把它们
替换成数据库中的实际 id，再调用对应的导入接口。只用标准库，便于离线运行。
"""
import json, os, subprocess, sys, urllib.request

BASE = os.environ.get("BASE", "http://127.0.0.1:8080")
PSQL = os.environ.get("PSQL_BIN", "/opt/postgresql-18/bin/psql")
DSN = os.environ.get("DATABASE_URL", "postgres://ccdev:ccdev@127.0.0.1:5432/ccdev?sslmode=disable")


def q(sql):
    out = subprocess.check_output([PSQL, DSN, "-tAc", sql], text=True).strip()
    return out.splitlines()[0] if out else ""


def login(user, pw):
    req = urllib.request.Request(f"{BASE}/api/login", method="POST",
                                 data=json.dumps({"username": user, "password": pw}).encode(),
                                 headers={"Content-Type": "application/json"})
    return json.loads(urllib.request.urlopen(req).read())["token"]


def post(path, payload, token):
    req = urllib.request.Request(f"{BASE}{path}", method="POST",
                                 data=json.dumps(payload).encode(),
                                 headers={"Content-Type": "application/json", "Authorization": f"Bearer {token}"})
    try:
        return json.loads(urllib.request.urlopen(req).read())
    except urllib.error.HTTPError as e:
        return json.loads(e.read())


def pile_id(code):
    return int(q(f"select id from rt.piles where code='{code}'"))


def trip_lot_id(batch):
    tid = trip_id(batch)
    return int(q(f"select id from rt.inventory_lots where lot_key='TRIP-{tid}'"))


def trip_id(batch):
    return int(q(f"select id from rt.trips where batch_no='{batch}'"))


def main():
    yard = login("yard", "yard123")
    lab = login("lab", "lab123")

    print("== 磅秤报文 ==")
    with open("samples/scale/2601-trucks.json") as f:
        scale = json.load(f)
    r = post("/api/ingest/scale", scale, yard)
    print({k: r[k] for k in ("accepted", "duplicate", "rejected")})

    print("== 样品档案（含一份缺车次对照） ==")
    with open("samples/assays/2601-samples.json") as f:
        assays = json.load(f)
    print(post("/api/ingest/assays", assays, lab))

    print("== 转移单（取当前有余额的批次，各取一半均匀合堆到 USABLE-2） ==")
    # 选当前有余额的批次；隔离料只能去隔离堆（门禁会拒绝进可用堆），
    # 其余料去可用堆。各取一半，保持“回同一物理堆位也是合法新节点”的演示。
    rows = q("""select l.id, l.weight_g,
                  case when p.kind='isolated' then 'isolated' else 'plain' end
                from rt.inventory_lots l join rt.piles p on p.id=l.pile_id
                where l.weight_g>1000 order by l.id limit 2""").split("\n")
    parsed = []
    for line in rows:
        parts = line.split("|")
        if len(parts) == 3:
            parsed.append((int(parts[0]), int(parts[1]), parts[2]))
    if not parsed:
        print("  当前没有可转移的余额，跳过。")
        return
    isolated = all(p[2] == "isolated" for p in parsed)
    dest = pile_id("ISO-1") if isolated else pile_id("USABLE-2")
    srcs = [{"lot_id": lid, "weight_g": w // 2} for lid, w, _ in parsed]
    total = sum(s["weight_g"] for s in srcs)
    move = {
        "external_ref": "LOAD-MIX-1", "kind": "split", "mode": "uniform_share",
        "note": "样例文件导入：均匀拆分/合堆",
        "sources": srcs,
        "destinations": [{"pile_id": dest, "weight_g": total, "grade_code": "CU-BRIGHT"}],
    }
    r = post("/api/transfers", move, yard)
    print({"ref": r.get("external_ref"), "lines": len(r.get("lines", [])), "error": r.get("error")})


if __name__ == "__main__":
    main()
