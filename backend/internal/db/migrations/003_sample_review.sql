-- 封样留样、封签流转、供应商/厂内复核、差异裁定与结算影响。
-- 关键约束：
--   * 同一留样（sample_units）只能被外送检测领取一次（sample_draws.unit_id 唯一）；
--   * 封签断裂 / 样量不足 / 已领取的留样不能再安排复核；
--   * 一份留样可先分样，再按分样单元分别领取；
--   * 外送结果以盲编号关联，揭示前不返回供应商报价；
--   * 裁定后的新增报告只能触发重新复核，不能覆盖原裁定。

create table if not exists rt.sample_units (
  id bigserial primary key,
  unit_no text not null unique,
  sample_id bigint not null references rt.samples(id) on delete cascade,
  parent_unit_id bigint references rt.sample_units(id),
  origin text not null check (origin in ('original','split')),
  position text not null default '',
  seal_no text not null default '',
  initial_g bigint not null check (initial_g > 0),
  remaining_g bigint not null check (remaining_g >= 0),
  status text not null default 'sealed' check (status in ('sealed','drawn','broken','void')),
  note text not null default '',
  created_by bigint references rt.users(id),
  created_at timestamptz not null default now(),
  check (remaining_g <= initial_g)
);
create index if not exists sample_units_sample_idx on rt.sample_units(sample_id);
create index if not exists sample_units_parent_idx on rt.sample_units(parent_unit_id);

-- 封签流转：原封、分样、换封、领取、断封都留一条事件。
create table if not exists rt.sample_seal_events (
  id bigserial primary key,
  unit_id bigint not null references rt.sample_units(id) on delete cascade,
  event text not null check (event in ('sealed','split','reseal','draw','broken')),
  seal_no text not null default '',
  previous_seal text not null default '',
  weight_g bigint not null default 0,
  detail jsonb not null default '{}'::jsonb,
  actor_id bigint references rt.users(id),
  occurred_at timestamptz not null default now()
);
create index if not exists sample_seal_events_unit_idx on rt.sample_seal_events(unit_id);

-- 复核案件：一车一份封样（可分样）与供应商报告、厂内报告的比对。
create table if not exists rt.review_cases (
  id bigserial primary key,
  case_no text not null unique,
  trip_id bigint not null references rt.trips(id),
  supplier_id bigint not null references rt.suppliers(id),
  sample_id bigint not null references rt.samples(id),
  sample_unit_id bigint references rt.sample_units(id),
  status text not null default 'open'
    check (status in ('open','comparing','pending_evidence','adjudicated','settled')),
  same_position boolean not null default true,
  metal_tolerance_ppm bigint not null default 2000 check (metal_tolerance_ppm >= 0),
  moisture_tolerance_ppm bigint not null default 2000 check (moisture_tolerance_ppm >= 0),
  evidence_version int not null default 1,
  opened_by bigint references rt.users(id),
  opened_at timestamptz not null default now(),
  note text not null default ''
);
create index if not exists review_cases_trip_idx on rt.review_cases(trip_id);

-- 外送检测领取：unit_id 唯一 => 同一留样不能被多次复核重复使用。
create table if not exists rt.sample_draws (
  id bigserial primary key,
  unit_id bigint not null unique references rt.sample_units(id),
  case_id bigint not null references rt.review_cases(id),
  purpose text not null default 'external_check',
  weight_g bigint not null check (weight_g > 0),
  blind_code text not null unique,
  lab_name text not null default '',
  drawn_by bigint not null references rt.users(id),
  drawn_at timestamptz not null default now()
);

-- 供应商报告 / 厂内报告。值统一按 ppm 存储，declared_unit 记录原始单位，
-- unit_ok=false 表示单位未登记（不可比，转待补证）。
create table if not exists rt.party_reports (
  id bigserial primary key,
  case_id bigint not null references rt.review_cases(id) on delete cascade,
  party text not null check (party in ('supplier','plant')),
  report_no text not null,
  lab_name text not null default '',
  sample_no text not null default '',
  seal_no text not null default '',
  split_from text not null default '',
  position text not null default '',
  declared_unit text not null default '',
  unit_ok boolean not null default true,
  moisture_ppm bigint not null default 0,
  impurity_ppm bigint,
  impurity_basis text not null default '',
  metal_ppm bigint not null default 0,
  metal_basis text not null default '',
  quote_cents_per_t bigint not null default 0,
  relation_ok boolean not null default true,
  received_at timestamptz not null default now(),
  created_by bigint references rt.users(id),
  unique (case_id, party, report_no)
);
create index if not exists party_reports_case_idx on rt.party_reports(case_id);

-- 外送检测机构返回：以盲编号关联，揭示前不携带供应商报价。
create table if not exists rt.external_reports (
  id bigserial primary key,
  case_id bigint not null references rt.review_cases(id) on delete cascade,
  blind_code text not null unique,
  lab_name text not null default '',
  sample_unit_id bigint references rt.sample_units(id),
  draw_id bigint references rt.sample_draws(id),
  status text not null default 'awaiting'
    check (status in ('awaiting','returned','revealed','pending_evidence')),
  match_status text not null default 'linked'
    check (match_status in ('linked','pending_evidence')),
  declared_unit text not null default '',
  unit_ok boolean not null default true,
  moisture_ppm bigint not null default 0,
  impurity_ppm bigint,
  impurity_basis text not null default '',
  metal_ppm bigint not null default 0,
  metal_basis text not null default '',
  position text not null default '',
  sample_no text not null default '',
  seal_no text not null default '',
  received_at timestamptz,
  revealed boolean not null default false,
  revealed_by bigint references rt.users(id),
  revealed_at timestamptz,
  note text not null default ''
);

-- 裁定：明确采用哪一方结果、适用范围（车次）与理由；保留历史，不覆盖。
create table if not exists rt.adjudications (
  id bigserial primary key,
  case_id bigint not null references rt.review_cases(id),
  decision_seq int not null,
  adopted text not null check (adopted in ('supplier','plant','external','none')),
  party_report_id bigint references rt.party_reports(id),
  external_report_id bigint references rt.external_reports(id),
  scope_trip_ids bigint[] not null default '{}',
  comparable boolean not null default false,
  within_tolerance boolean not null default false,
  difference_sources jsonb not null default '[]'::jsonb,
  reason text not null,
  evidence_version int not null,
  status text not null default 'effective' check (status in ('effective','superseded','void')),
  adopted_assay_ids bigint[] not null default '{}',
  decided_by bigint not null references rt.users(id),
  decided_at timestamptz not null default now(),
  unique (case_id, decision_seq)
);

-- 裁定试算/结算的逐车结果：旧款、新款、差额、结算方式。
create table if not exists rt.adjudication_lines (
  id bigserial primary key,
  adjudication_id bigint not null references rt.adjudications(id) on delete cascade,
  trip_id bigint not null references rt.trips(id),
  old_amount_cents bigint not null default 0,
  new_amount_cents bigint not null default 0,
  delta_cents bigint not null default 0,
  settlement text not null default 'pending'
    check (settlement in ('pending','recovery','rebuild','blocked')),
  payable_id bigint references rt.payables(id),
  note text not null default '',
  unique (adjudication_id, trip_id)
);

-- 争议只冻结对应车次，不影响同一供应商其他车。
create table if not exists rt.payment_freezes (
  id bigserial primary key,
  case_id bigint not null references rt.review_cases(id),
  trip_id bigint not null references rt.trips(id),
  reason text not null,
  status text not null default 'active' check (status in ('active','released')),
  frozen_by bigint references rt.users(id),
  frozen_at timestamptz not null default now(),
  released_at timestamptz
);
create unique index if not exists payment_freezes_active_ux
  on rt.payment_freezes(trip_id) where status='active';

create table if not exists rt.rereview_requests (
  id bigserial primary key,
  case_id bigint not null references rt.review_cases(id),
  adjudication_id bigint references rt.adjudications(id),
  reason text not null,
  status text not null default 'open' check (status in ('open','closed')),
  requested_by bigint references rt.users(id),
  requested_at timestamptz not null default now(),
  closed_at timestamptz
);
create index if not exists rereview_open_idx on rt.rereview_requests(case_id) where status='open';

-- 已付形成差额追补/追回；未付重建付款依据（新 payable 版本）。
create table if not exists rt.payment_recoveries (
  id bigserial primary key,
  case_id bigint not null references rt.review_cases(id),
  adjudication_id bigint not null references rt.adjudications(id),
  trip_id bigint not null references rt.trips(id),
  kind text not null check (kind in ('recovery','rebuild')),
  delta_cents bigint not null,
  base_payable_id bigint references rt.payables(id),
  new_payable_id bigint references rt.payables(id),
  note text not null default '',
  created_by bigint references rt.users(id),
  created_at timestamptz not null default now()
);

-- 裁定形成的新化验版本，记录来源，便于追溯是“哪份结果”用于结算。
alter table rt.assay_results add column if not exists source text not null default 'manual';
alter table rt.assay_results add column if not exists adjudication_id bigint references rt.adjudications(id);
create index if not exists assays_adjudication_idx on rt.assay_results(adjudication_id);
