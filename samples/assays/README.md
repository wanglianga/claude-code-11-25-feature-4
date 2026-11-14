# 样品档案样例

`assays/*.json` 为项目自带的样品档案样例，字段与 `POST /api/ingest/assays` 一致。
每份记录**必须**带 `trip_key` 与 `position`（取样位置），否则平台拒收——
缺少车次归属的化验不会被按照供应商+日期自动分配到某一车。

- `moisture_pct`、`impurity_pct`、`metal_pct`：百分数，平台按 ppm 存储。
- `impurity_basis`、`metal_basis`：`dry` 或 `wet`，必须显式声明；合同公式据此判断
  是否需要在干基/湿基之间换算，并记录换算过程。
