# 转移单样例

`transfers/*.json` 为项目自带的转移单样例，字段与 `POST /api/transfers` 一致。

不变量（服务端强制）：

1. `out 重量 − 明确损耗重量 = in 重量`，必须精确成立，否则整单阻断。
2. 每条 `sources` 重量不得超过该批次当前余额（禁止超库存）。
3. `dests` 指向 `usable` 堆时，每个来源车次都必须有已批准且非隔离的等级。
4. `external_ref` 是幂等键：重放同一单号返回原移动，不会再移动材料。
5. `losses.loss_class`：`drying` 只减重量不减金属；`process` 同时按比例减金属。
6. `mode`：
   - `uniform_share`：声明均匀混合，按各来源在整堆当次的构成占比拆分，尾差明确记录；
   - `measured`：非均匀分拣，必须为每条转出行给出 `dest_pile_id` 和 `origins` 来源实测；
   - `unknown`：去向明确但来源比例未知，库存标记 `metal_known=false`、界面标“来源比例未知”。

转移单使用 `lot_id`（库存批次节点的数字 id）。节点在每次动作后生成新的，
因此料回到同一物理堆位是新的合法节点，不会因同名堆位被判环。
