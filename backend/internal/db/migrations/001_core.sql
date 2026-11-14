-- 再生金属来料与料堆追溯平台 —— 核心主数据、车次、称重、封样、化验
-- 单位约定：重量 g、金属量 mg、比例 ppm、金额 分。
create schema if not exists rt;

create table if not exists rt.users (
  id bigserial primary key,
  username text not null unique,
  display_name text not null,
  role text not null check (role in ('admin','purchasing','yard','lab','quality','finance')),
  password_hash text not null,
  created_at timestamptz not null default now()
);

create table if not exists rt.sessions (
  token text primary key,
  user_id bigint not null references rt.users(id) on delete cascade,
  expires_at timestamptz not null,
  created_at timestamptz not null default now()
);

create table if not exists rt.suppliers (
  id bigserial primary key,
  code text not null unique,
  name text not null
);

create table if not exists rt.contracts (
  id bigserial primary key,
  code text not null unique,
  supplier_id bigint not null references rt.suppliers(id),
  name text not null,
  formula jsonb not null,
  metal_basis text not null check (metal_basis in ('dry','wet')),
  unit_price_cents_per_t bigint not null check (unit_price_cents_per_t > 0),
  active boolean not null default true,
  effective_from date not null default current_date,
  note text not null default '',
  created_at timestamptz not null default now()
);

create table if not exists rt.contract_grades (
  contract_id bigint not null references rt.contracts(id) on delete cascade,
  grade_code text not null,
  coef_ppm bigint not null check (coef_ppm between 1 and 2000000),
  intake_allowed boolean not null default true,
  primary key (contract_id, grade_code)
);

create table if not exists rt.trips (
  id bigserial primary key,
  batch_no text not null unique,
  supplier_id bigint not null references rt.suppliers(id),
  contract_id bigint not null references rt.contracts(id),
  plate_no text not null,
  driver text not null default '',
  arrival_at timestamptz not null,
  note text not null default '',
  created_at timestamptz not null default now()
);

create table if not exists rt.weighings (
  id bigserial primary key,
  trip_id bigint not null references rt.trips(id),
  kind text not null check (kind in ('gross','tare')),
  seq int not null,
  weight_g bigint not null check (weight_g > 0),
  weighed_at timestamptz not null,
  scale_device text not null default '',
  ticket_no text not null unique,
  source text not null default 'scale-report',
  is_void boolean not null default false,
  void_reason text not null default '',
  created_at timestamptz not null default now(),
  unique (trip_id, kind, seq)
);

-- 毛重/皮重采用记录：复称保留原值，同时留下采用依据；同一车次仅一条 current。
create table if not exists rt.weight_adoptions (
  id bigserial primary key,
  trip_id bigint not null references rt.trips(id),
  gross_weighing_id bigint not null references rt.weighings(id),
  tare_weighing_id bigint not null references rt.weighings(id),
  net_g bigint not null check (net_g > 0),
  basis_note text not null default '',
  adopted_by bigint not null references rt.users(id),
  adopted_at timestamptz not null default now(),
  is_current boolean not null default true,
  supersedes_id bigint references rt.weight_adoptions(id)
);
create unique index if not exists weight_adoptions_current_ux
  on rt.weight_adoptions(trip_id) where is_current;

-- 封样必须直接绑定车次与取样位置，不允许无归属化验按日期自动分配。
create table if not exists rt.samples (
  id bigserial primary key,
  sample_no text not null unique,
  trip_id bigint not null references rt.trips(id),
  position text not null,
  seal_no text not null,
  sealed_at timestamptz not null,
  sealed_by bigint references rt.users(id),
  note text not null default '',
  created_at timestamptz not null default now(),
  unique (trip_id, position, seal_no)
);

create table if not exists rt.assay_results (
  id bigserial primary key,
  sample_id bigint not null references rt.samples(id),
  version int not null,
  moisture_ppm bigint not null check (moisture_ppm between 0 and 1000000),
  impurity_ppm bigint check (impurity_ppm between 0 and 1000000),
  impurity_basis text check (impurity_basis in ('dry','wet')),
  metal_ppm bigint not null check (metal_ppm between 0 and 1000000),
  metal_basis text not null check (metal_basis in ('dry','wet')),
  inspector text not null default '',
  submitted_at timestamptz not null default now(),
  quality_status text not null default 'pending'
    check (quality_status in ('pending','adopted','rejected','superseded')),
  quality_by bigint references rt.users(id),
  quality_at timestamptz,
  quality_note text not null default '',
  raw_ref text not null default '',
  unique (sample_id, version)
);

create table if not exists rt.grade_approvals (
  id bigserial primary key,
  trip_id bigint not null unique references rt.trips(id),
  grade_code text not null,
  coef_ppm bigint not null,
  isolated boolean not null default false,
  status text not null check (status in ('pending','approved','rejected')),
  approved_by bigint references rt.users(id),
  approved_at timestamptz,
  note text not null default '',
  updated_at timestamptz not null default now()
);

create index if not exists trips_supplier_idx on rt.trips(supplier_id);
create index if not exists weighings_trip_idx on rt.weighings(trip_id);
create index if not exists samples_trip_idx on rt.samples(trip_id);
create index if not exists assays_sample_idx on rt.assay_results(sample_id);
