-- 库存、来源关系、转移单、应付款与调整。全部使用整数单位确保守恒。
create table if not exists rt.piles (
  id bigserial primary key,
  code text not null unique,
  name text not null,
  kind text not null check (kind in ('receiving','usable','isolated','production','shipped','loss')),
  physical_location text not null default '',
  created_at timestamptz not null default now()
);

-- 车次汇总采用结果：记录质量人员采纳的化验。同一车次仅一条。
create table if not exists rt.trip_assay_adoptions (
  trip_id bigint primary key references rt.trips(id),
  assay_id bigint not null references rt.assay_results(id),
  adopted_by bigint not null references rt.users(id),
  adopted_at timestamptz not null default now(),
  note text not null default ''
);

-- 库存批次。每个批次携带完整来源账，混料与拆分继承。
create table if not exists rt.inventory_lots (
  id bigserial primary key,
  lot_key text not null unique,
  pile_id bigint not null references rt.piles(id),
  trip_id bigint references rt.trips(id),
  grade_code text not null default '',
  weight_g bigint not null default 0 check (weight_g >= 0),
  metal_mg bigint not null default 0 check (metal_mg >= 0),
  moisture_ppm bigint not null default 0,
  metal_known boolean not null default false,
  state text not null default 'available'
    check (state in ('available','held','consumed','shipped','written_off')),
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now()
);
create index if not exists inventory_lots_pile_idx on rt.inventory_lots(pile_id);
create index if not exists inventory_lots_trip_idx on rt.inventory_lots(trip_id);

-- 每个批次的车次级构成：按整堆当次构成分配，显式记录尾差归属。
create table if not exists rt.lot_origins (
  lot_id bigint not null references rt.inventory_lots(id) on delete cascade,
  trip_id bigint not null references rt.trips(id),
  weight_g bigint not null check (weight_g >= 0),
  metal_mg bigint not null default 0 check (metal_mg >= 0),
  share_ppm bigint not null check (share_ppm between 0 and 1000000),
  basis text not null default 'uniform_share'
    check (basis in ('uniform_share','measured','unknown')),
  remainder_g bigint not null default 0,
  remainder_mg bigint not null default 0,
  primary key (lot_id, trip_id)
);
create index if not exists lot_origins_trip_idx on rt.lot_origins(trip_id);

-- 来源关系：每次拆出、合堆、出库都写成 from -> to 的带数量边。
create table if not exists rt.lot_links (
  id bigserial primary key,
  movement_id bigint,
  from_lot_id bigint not null references rt.inventory_lots(id),
  to_lot_id bigint not null references rt.inventory_lots(id),
  weight_g bigint not null check (weight_g > 0),
  metal_mg bigint not null check (metal_mg >= 0),
  share_ppm bigint not null check (share_ppm between 0 and 1000000),
  basis text not null check (basis in ('uniform_share','measured','unknown')),
  remainder_g bigint not null default 0,
  remainder_mg bigint not null default 0,
  created_at timestamptz not null default now(),
  check (from_lot_id <> to_lot_id)
);
create index if not exists lot_links_from_idx on rt.lot_links(from_lot_id);
create index if not exists lot_links_to_idx on rt.lot_links(to_lot_id);

create table if not exists rt.movements (
  id bigserial primary key,
  kind text not null check (kind in ('inbound','mix','split','ship','loss','write_off','return')),
  external_ref text not null unique,     -- 转移单号；幂等键，重放不移动材料
  occurred_at timestamptz not null,
  actor_id bigint not null references rt.users(id),
  note text not null default '',
  created_at timestamptz not null default now()
);

-- 转移单明细：显式转出、转入、明确损耗。out = in + loss 必须精确成立。
create table if not exists rt.transfer_lines (
  id bigserial primary key,
  movement_id bigint not null references rt.movements(id) on delete cascade,
  lot_id bigint not null references rt.inventory_lots(id),
  direction text not null check (direction in ('out','in','loss')),
  weight_g bigint not null check (weight_g > 0),
  metal_mg bigint not null default 0 check (metal_mg >= 0),
  basis text not null default 'uniform_share'
    check (basis in ('uniform_share','measured','unknown')),
  loss_class text not null default '' check (loss_class in ('','drying','process')),
  note text not null default ''
);

-- 每次动作后的库存状态节点，供追溯时间线展示。
create table if not exists rt.lot_events (
  id bigserial primary key,
  lot_id bigint not null references rt.inventory_lots(id) on delete cascade,
  movement_id bigint references rt.movements(id),
  event text not null,
  weight_g bigint not null,
  metal_mg bigint not null,
  pile_id bigint references rt.piles(id),
  detail jsonb not null default '{}'::jsonb,
  occurred_at timestamptz not null,
  created_at timestamptz not null default now()
);
create index if not exists lot_events_lot_idx on rt.lot_events(lot_id);

-- 应付款版本：原单永远保留；调整追加新版本并挂 supersedes_id。
create table if not exists rt.payables (
  id bigserial primary key,
  trip_id bigint not null references rt.trips(id),
  version int not null,
  is_current boolean not null default false,
  supersedes_id bigint references rt.payables(id),
  weight_adoption_id bigint references rt.weight_adoptions(id),
  assay_id bigint references rt.assay_results(id),
  grade_approval_id bigint references rt.grade_approvals(id),
  contract_id bigint references rt.contracts(id),
  formula_snapshot jsonb not null default '{}'::jsonb,
  wet_net_g bigint not null default 0,
  dry_g bigint not null default 0,
  impurity_g bigint not null default 0,
  priced_g bigint not null default 0,
  metal_mg bigint not null default 0,
  coef_ppm bigint not null default 0,
  unit_price_cents_per_t bigint not null default 0,
  amount_cents bigint not null default 0,
  status text not null check (status in ('calculated','approved','paid','held','superseded','void')),
  hold_reason text not null default '',
  computed_by bigint not null references rt.users(id),
  computed_at timestamptz not null default now(),
  approved_by bigint references rt.users(id),
  approved_at timestamptz,
  note text not null default '',
  unique (trip_id, version)
);
create unique index if not exists payables_current_ux on rt.payables(trip_id) where is_current;

create table if not exists rt.payment_ledger (
  id bigserial primary key,
  payable_id bigint not null references rt.payables(id),
  trip_id bigint not null references rt.trips(id),
  delta_cents bigint not null,
  old_metal_mg bigint not null default 0,
  new_metal_mg bigint not null default 0,
  old_wet_net_g bigint not null default 0,
  new_wet_net_g bigint not null default 0,
  reason text not null,
  actor_id bigint not null references rt.users(id),
  created_at timestamptz not null default now()
);

create table if not exists rt.audit_log (
  id bigserial primary key,
  actor_id bigint references rt.users(id),
  action text not null,
  entity text not null,
  entity_id text not null,
  detail jsonb not null default '{}'::jsonb,
  created_at timestamptz not null default now()
);

-- 损耗按车次的归属：使逐车守恒报表能把干燥损失与加工损失分别落到来源车次，
-- 不必在报表端按比例重新估算。
create table if not exists rt.loss_attribution (
  id bigserial primary key,
  movement_id bigint not null references rt.movements(id) on delete cascade,
  trip_id bigint not null references rt.trips(id),
  loss_class text not null check (loss_class in ('drying','process')),
  weight_g bigint not null check (weight_g >= 0),
  metal_mg bigint not null default 0 check (metal_mg >= 0),
  created_at timestamptz not null default now()
);
create index if not exists loss_attr_trip_idx on rt.loss_attribution(trip_id);
