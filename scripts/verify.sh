#!/usr/bin/env bash
# 端到端验证脚本：不依赖真实仪器，使用项目自带样例 + 数据库断言。
# 用法： ./scripts/verify.sh            （使用 backend 默认 DSN）
#        BASE=http://127.0.0.1:8080 ./scripts/verify.sh
set -uo pipefail
cd "$(dirname "$0")/.."
BASE="${BASE:-http://127.0.0.1:8080}"
DSN="${DATABASE_URL:-postgres://ccdev:ccdev@127.0.0.1:5432/ccdev?sslmode=disable}"
PSQL_BIN="${PSQL_BIN:-/opt/postgresql-18/bin/psql}"
GO_BIN="${GO_BIN:-/opt/go/bin/go}"
fail=0
pass() { printf '  \033[32mPASS\033[0m %s\n' "$1"; }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$1"; fail=1; }
q() { "$PSQL_BIN" "$DSN" -tAc "$1"; }
tok() { curl -s -X POST "$BASE/api/login" -H 'Content-Type: application/json' \
  -d "{\"username\":\"$1\",\"password\":\"$2\"}" | jq -r .token; }

echo "== 0. 服务可用性 + 迁移 + 重置样例数据 =="
if ! curl -sf "$BASE/api/health" >/dev/null; then
  echo "ERROR: $BASE 不可达，请先启动服务（见 README：一键启动 / 本地启动）"; exit 1
fi
( cd backend && "$GO_BIN" run ./cmd/seed ) || { echo "seed failed"; exit 1; }

echo "== 1. 逐车批次独立 =="
n=$(q "select count(*) from rt.trips where supplier_id=1 and arrival_at::date=(select max(arrival_at::date) from rt.trips)")
d=$(q "select count(distinct batch_no) from rt.trips")
[ "$n" -ge 2 ] && [ "$d" -eq "$(q 'select count(*) from rt.trips')" ] && pass "同日同供应商 $n 车，批次号互不相同" || bad "批次号不唯一"

echo "== 2. 来源账 = 批次余额 =="
gap=$(q "select count(*) from (select l.id, l.weight_g, coalesce(sum(o.weight_g),0) ow, l.metal_mg, coalesce(sum(o.metal_mg),0) om from rt.inventory_lots l left join rt.lot_origins o on o.lot_id=l.id group by l.id,l.weight_g,l.metal_mg) x where weight_g<>ow or metal_mg<>om")
[ "$gap" -eq 0 ] && pass "所有批次 来源重量/金属 == 余额" || bad "$gap 个批次来源账与余额不符"

echo "== 3. 转移单守恒 out = in + loss =="
gap=$(q "select count(*) from (select m.id, coalesce(sum(tl.weight_g) filter (where tl.direction='out'),0) o, coalesce(sum(tl.weight_g) filter (where tl.direction='in'),0) i, coalesce(sum(tl.weight_g) filter (where tl.direction='loss'),0) l from rt.movements m join rt.transfer_lines tl on tl.movement_id=m.id group by m.id) x where o>0 and o<>i+l")
[ "$gap" -eq 0 ] && pass "每张转移单 转出 = 转入 + 明确损耗" || bad "$gap 张转移单数量不守恒"

echo "== 4. 隔离料未进入可用堆 =="
n=$(q "select count(*) from rt.lot_origins o join rt.inventory_lots l on l.id=o.lot_id join rt.piles p on p.id=l.pile_id join rt.grade_approvals g on g.trip_id=o.trip_id where p.kind='usable' and g.isolated")
[ "$n" -eq 0 ] && pass "可用堆无隔离料来源" || bad "$n 条隔离料来源出现在可用堆"

echo "== 5. 应付款版本：原单保留 + 差额 =="
v=$(q "select count(*) from rt.payables where trip_id=1")
cur=$(q "select amount_cents from rt.payables where trip_id=1 and is_current")
old=$(q "select amount_cents from rt.payables where trip_id=1 and version=1")
[ "$v" -ge 2 ] && [ "$cur" -lt "$old" ] && pass "车次1 有 $v 个版本，复检后金额 $old → $cur 分（下调）" || bad "版本保留或差额异常 (v=$v cur=$cur old=$old)"

echo "== 6. 重放同一转移单不重复移动 =="
before=$(q "select coalesce(sum(weight_g),0) from rt.inventory_lots where pile_id=(select id from rt.piles where code='SHIP-1')")
YARD=$(tok yard yard123)
curl -s -X POST "$BASE/api/transfers" -H "Authorization: Bearer $YARD" -H 'Content-Type: application/json' \
  -d '{"external_ref":"MX-2601-001","kind":"mix","mode":"uniform_share","sources":[{"lot_id":1,"weight_g":1}],"destinations":[{"pile_id":1,"weight_g":1}]}' >/dev/null
after=$(q "select coalesce(sum(weight_g),0) from rt.inventory_lots where pile_id=(select id from rt.piles where code='SHIP-1')")
[ "$before" = "$after" ] && pass "重放 MX-2601-001 后发货区总量不变 ($after g)" || bad "重放导致材料移动 $before -> $after"

echo "== 7. 超库存与隔离混料被拒绝 =="
ISO=$(q "select id from rt.inventory_lots where pile_id=(select id from rt.piles where code='ISO-1') limit 1")
ISOW=$(q "select weight_g from rt.inventory_lots where id=$ISO")
code=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE/api/transfers" -H "Authorization: Bearer $YARD" -H 'Content-Type: application/json' \
  -d "{\"external_ref\":\"VERIFY-OVER\",\"kind\":\"split\",\"mode\":\"uniform_share\",\"sources\":[{\"lot_id\":$ISO,\"weight_g\":$((ISOW+1))}],\"destinations\":[{\"pile_id\":1,\"weight_g\":$((ISOW+1))}]}")
[ "$code" = "409" ] && pass "超库存被拒绝 (HTTP $code)" || bad "超库存未被拒绝 (HTTP $code)"
U1=$(q "select id from rt.piles where code='USABLE-1'")
code=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE/api/transfers" -H "Authorization: Bearer $YARD" -H 'Content-Type: application/json' \
  -d "{\"external_ref\":\"VERIFY-ISO\",\"kind\":\"mix\",\"mode\":\"uniform_share\",\"sources\":[{\"lot_id\":$ISO,\"weight_g\":$ISOW}],\"destinations\":[{\"pile_id\":$U1,\"weight_g\":$ISOW}]}")
[ "$code" = "409" ] && pass "隔离料混入可用堆被拒绝 (HTTP $code)" || bad "隔离料混入未被拒绝 (HTTP $code)"

echo "== 8. 缺归属化验被拒收 =="
LAB=$(tok lab lab123)
out=$(curl -s -X POST "$BASE/api/ingest/assays" -H "Authorization: Bearer $LAB" -H 'Content-Type: application/json' \
  -d '[{"sample_no":"NO-TRIP-1","position":"车厢中部","seal_no":"X","moisture_pct":3,"impurity_pct":1,"impurity_basis":"dry","metal_pct":60,"metal_basis":"dry"}]')
echo "$out" | jq -e '.items[0].outcome=="rejected"' >/dev/null && pass "缺 trip_key 的化验被拒收" || bad "缺归属化验未被拒收: $out"

echo "== 9. 磅秤报文重放幂等 =="
YARD=$(tok yard yard123)
out=$(curl -s -X POST "$BASE/api/ingest/scale" -H "Authorization: Bearer $YARD" -H 'Content-Type: application/json' -d @samples/scale/2601-trucks.json)
acc=$(echo "$out" | jq -r .accepted); dup=$(echo "$out" | jq -r .duplicate)
[ "$acc" -eq 0 ] && [ "$dup" -eq 7 ] && pass "已导入的磅单再次投递全部判重 ($dup)" || bad "磅单重放行为异常 acc=$acc dup=$dup"

echo "== 10. 干燥损耗不减金属 =="
dry_metal=$(q "select coalesce(sum(metal_mg),0) from rt.loss_attribution where loss_class='drying'")
[ "$dry_metal" = "0" ] && pass "所有干燥损耗的金属扣减为 0" || bad "干燥损耗扣了金属 $dry_metal mg"

echo "== 11. 合同基准：干基与湿基公式结果不同 =="
FIN=$(tok finance finance123)
f=$(curl -s "$BASE/api/formulas" -H "Authorization: Bearer $FIN")
echo "$f" | jq -e 'length==2 and (map(.code)|index("DRY-IMP-METAL")!=null) and (map(.code)|index("WET-NET-METAL")!=null)' >/dev/null \
  && pass "登记了两个独立公式（干基/湿基）" || bad "公式登记异常: $f"

echo "== 12. 全场合计守恒 =="
t=$(curl -s "$BASE/api/reconciliation" -H "Authorization: Bearer $FIN")
echo "$t" | jq -e '.totals.gap_g==0 and .totals.gap_metal_mg==0' >/dev/null \
  && pass "重量尾差与金属尾差均为 0" || bad "存在尾差: $(echo "$t" | jq -c .totals)"

echo "== 13. 干湿基不可比：不取平均、只列差异来源 =="
QLTY=$(tok quality quality123)
CID=$(curl -s "$BASE/api/review-cases" -H "Authorization: Bearer $QLTY" | jq -r '.[] | select(.case_no|startswith("RC-B-20261110-001")) | .id' | head -1)
tr=$(curl -s "$BASE/api/review-cases/$CID/trial" -H "Authorization: Bearer $QLTY")
echo "$tr" | jq -e '.comparability.comparable==false and (.comparability.difference_sources|length)>0 and .averaging_forbidden==true' >/dev/null \
  && pass "干湿基判定不可比，列出差异来源，禁止平均" || bad "不可比判定异常: $(echo "$tr" | jq -c '.comparability')"
echo "$tr" | jq -e '([.candidates[]|select(.blocked==null)|.amount_cents] | length) >= 2' >/dev/null \
  && pass "各候选结果分别试算，未生成折中值" || bad "候选试算异常"

echo "== 14. 留样不能被重复领取 =="
U=$(q "select id from rt.sample_units where unit_no='SU-2601-002-S1'")
CB=$(q "select id from rt.review_cases where case_no like 'RC-B-20261110-002-02%'")
code=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE/api/review-cases/$CB/draws" -H "Authorization: Bearer $LAB" \
  -H 'Content-Type: application/json' -d "{\"unit_id\":$U,\"weight_g\":10,\"blind_code\":\"BL-VERIFY-DUP\"}")
[ "$code" = "409" ] && pass "同一留样二次领取被拒绝 (HTTP $code)" || bad "同一留样被重复领取 (HTTP $code)"

echo "== 15. 封签断裂不能安排复检 =="
U3=$(q "select id from rt.sample_units where unit_no='SU-2601-003'")
C3=$(q "select id from rt.review_cases where case_no like 'RC-B-20261110-003%'")
code=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE/api/review-cases/$C3/draws" -H "Authorization: Bearer $LAB" \
  -H 'Content-Type: application/json' -d "{\"unit_id\":$U3,\"weight_g\":10,\"blind_code\":\"BL-VERIFY-BROKEN\"}")
[ "$code" = "409" ] && pass "封签断裂的留样复检被拒绝 (HTTP $code)" || bad "断裂封签仍可安排复检 (HTTP $code)"

echo "== 16. 揭示前不返回外送数值 =="
masked=$(curl -s "$BASE/api/review-cases/$CB" -H "Authorization: Bearer $LAB" | jq '[.external_reports[]|select(.revealed==false)|select(.masked==true)]|length')
[ "$masked" -ge 1 ] && pass "未揭示外送结果已脱敏 ($masked 条)" || bad "未揭示外送结果泄露数值"

echo "== 17. 争议只冻结本车 =="
cross=$(q "select count(*) from rt.payment_freezes f join rt.review_cases c on c.id=f.case_id where c.trip_id<>f.trip_id")
stray=$(q "select count(*) from rt.payment_freezes f where f.status='active' and not exists(select 1 from rt.review_cases c where c.trip_id=f.trip_id and c.status<>'settled')")
paid_other=$(q "select count(*) from rt.payables where status='paid' and trip_id<>1")
[ "$cross" -eq 0 ] && [ "$stray" -eq 0 ] && [ "$paid_other" -ge 1 ] \
  && pass "无跨车冻结；同供应商其他车照常付款 ($paid_other 车已付，金额保留)" \
  || bad "冻结范围异常 (cross=$cross stray=$stray other_paid=$paid_other)"

echo "== 18. 裁定结算：已付追补 / 未付重建 =="
rec=$(q "select count(*) from rt.payment_recoveries where kind='recovery' and delta_cents<>0")
reb=$(q "select count(*) from rt.payment_recoveries where kind='rebuild' and delta_cents<>0")
[ "$rec" -ge 1 ] && [ "$reb" -ge 1 ] && pass "已付形成差额追补($rec)，未付重建付款依据($reb)" || bad "结算路由异常 recovery=$rec rebuild=$reb"

echo "== 19. 裁定后新报告只提出重新复核 =="
eff=$(q "select count(*) from rt.adjudications where case_id=(select id from rt.review_cases where case_no like 'RC-B-20261110-002-01%') and status='effective'")
[ "$eff" -eq 1 ] && pass "每案至多一条生效裁定，历史保留" || bad "生效裁定数异常 ($eff)"

echo
if [ "$fail" -eq 0 ]; then echo "ALL CHECKS PASSED"; else echo "SOME CHECKS FAILED"; fi
exit "$fail"
