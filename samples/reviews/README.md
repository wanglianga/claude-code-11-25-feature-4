# 封样复核与差异裁定样例

`reviews/*.json` 为项目自带的复核样例，覆盖三类必须被识别的边界条件。
它们描述的是**同一车材料**上供应商报告、厂内报告、外送盲样之间的关系。

| 文件 | 场景 | 平台行为 |
| --- | --- | --- |
| `dry-wet-incomparable.json` | 供应商干基、厂内湿基 | 判定不可比，只列差异来源，不取平均、不自动择高/择低；需外送或明确采用一方并写理由 |
| `retain-competition.json` | 两个复核抢同一留样单元 | 同一单元只能领取一次；第二次被拒，改用分样/另一单元才成功 |
| `broken-seal.json` | 留样封签断裂 | 不能安排复检，剩余量清零，封签流转留痕，案件转待补证 |

对应接口：

- `POST /api/trips/{id}/review-cases` 开案（只冻结本车付款）
- `POST /api/review-cases/{id}/party-reports` 登记供应商/厂内报告
- `POST /api/review-cases/{id}/draws` 外送领取（一次占用并扣减样量）
- `POST /api/review-cases/{id}/external-return` 凭盲编号返回结果（不含供应商报价）
- `POST /api/review-cases/{id}/reveal` 正式揭示
- `GET  /api/review-cases/{id}/trial` 试算各候选结果与混堆去向
- `POST /api/review-cases/{id}/adjudicate` 裁定并结算

样本数据已在 `cmd/seed` 内置写入，直接运行即可在“封样复核”页面看到这三类场景。
