package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

func (a *App) GetLot(ctx context.Context, id int64) (Lot, error) {
	var l Lot
	err := a.DB.Pool.QueryRow(ctx, `select l.id, l.lot_key, l.pile_id, p.name, p.kind, l.trip_id,
		coalesce(t.batch_no,''), l.grade_code, l.weight_g, l.metal_mg, l.moisture_ppm, l.metal_known, l.state,
		to_char(l.created_at,'YYYY-MM-DD"T"HH24:MI:SSOF'), to_char(l.updated_at,'YYYY-MM-DD"T"HH24:MI:SSOF')
		from rt.inventory_lots l join rt.piles p on p.id=l.pile_id
		left join rt.trips t on t.id=l.trip_id where l.id=$1`, id).
		Scan(&l.ID, &l.Key, &l.PileID, &l.PileName, &l.PileKind, &l.TripID, &l.BatchNo, &l.GradeCode,
			&l.WeightG, &l.MetalMg, &l.MoisturePPM, &l.MetalKnown, &l.State, &l.CreatedAt, &l.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return l, notFound("批次 %d", id)
		}
		return l, err
	}
	orig, err := a.origins(ctx, id)
	if err != nil {
		return l, err
	}
	l.Origins = orig
	return l, nil
}

func (a *App) origins(ctx context.Context, lotID int64) ([]Origin, error) {
	rows, err := a.DB.Pool.Query(ctx, `select o.trip_id, coalesce(t.batch_no,''), o.weight_g, o.metal_mg, o.share_ppm,
		o.basis, o.remainder_g, o.remainder_mg
		from rt.lot_origins o left join rt.trips t on t.id=o.trip_id
		where o.lot_id=$1 and o.weight_g > 0 order by o.trip_id`, lotID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Origin{}
	for rows.Next() {
		var o Origin
		if err := rows.Scan(&o.TripID, &o.BatchNo, &o.WeightG, &o.MetalMg, &o.SharePPM, &o.Basis,
			&o.RemainderG, &o.RemainderMg); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

func (a *App) ListLots(ctx context.Context, pileID int64) ([]Lot, error) {
	q := `select id from rt.inventory_lots`
	args := []any{}
	if pileID > 0 {
		q += ` where pile_id=$1`
		args = append(args, pileID)
	}
	q += ` order by id`
	rows, err := a.DB.Pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	out := []Lot{}
	for _, id := range ids {
		l, err := a.GetLot(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, nil
}

// LotState is the stock position of one physical pile: what is on it now, and
// which lots (state nodes) built it.
type LotState struct {
	Pile         Pile     `json:"pile"`
	WeightG      int64    `json:"weight_g"`
	MetalMg      int64    `json:"metal_mg"`
	Lots         []Lot    `json:"lots"`
	Origins      []Origin `json:"origins"`
	UnknownShare bool     `json:"unknown_share"`
}

func (a *App) StockByPile(ctx context.Context) ([]LotState, error) {
	piles, err := a.ListPiles(ctx)
	if err != nil {
		return nil, err
	}
	states := make([]LotState, 0, len(piles))
	for _, p := range piles {
		lots, err := a.ListLots(ctx, p.ID)
		if err != nil {
			return nil, err
		}
		st := LotState{Pile: p, Lots: []Lot{}, Origins: []Origin{}}
		originW := map[int64]int64{}
		originM := map[int64]int64{}
		originBasis := map[int64]string{}
		batch := map[int64]string{}
		for _, l := range lots {
			if l.WeightG <= 0 {
				continue
			}
			st.WeightG += l.WeightG
			st.MetalMg += l.MetalMg
			for _, o := range l.Origins {
				originW[o.TripID] += o.WeightG
				originM[o.TripID] += o.MetalMg
				batch[o.TripID] = o.BatchNo
				if cur, ok := originBasis[o.TripID]; ok && cur != o.Basis {
					originBasis[o.TripID] = "unknown"
				} else if !ok {
					originBasis[o.TripID] = o.Basis
				}
				if o.Basis == "unknown" {
					st.UnknownShare = true
				}
			}
		}
		// Drop lots fully consumed (weight 0) from the display list.
		visible := lots[:0]
		for _, l := range lots {
			if l.WeightG > 0 || l.State != "consumed" {
				visible = append(visible, l)
			}
		}
		st.Lots = visible
		for tid, w := range originW {
			if w <= 0 {
				continue
			}
			share := int64(0)
			if st.WeightG > 0 {
				share = int64(w) * 1_000_000 / st.WeightG
			}
			st.Origins = append(st.Origins, Origin{TripID: tid, BatchNo: batch[tid], WeightG: w,
				MetalMg: originM[tid], SharePPM: share, Basis: originBasis[tid]})
		}
		states = append(states, st)
	}
	return states, nil
}

type Link struct {
	ID         int64  `json:"id"`
	MovementID *int64 `json:"movement_id"`
	FromLotID  int64  `json:"from_lot_id"`
	FromKey    string `json:"from_lot_key"`
	ToLotID    int64  `json:"to_lot_id"`
	ToKey      string `json:"to_lot_key"`
	WeightG    int64  `json:"weight_g"`
	MetalMg    int64  `json:"metal_mg"`
	SharePPM   int64  `json:"share_ppm"`
	Basis      string `json:"basis"`
}

func (a *App) LinksFrom(ctx context.Context, lotID int64) ([]Link, error) {
	return a.queryLinks(ctx, `select lk.id, lk.movement_id, lk.from_lot_id, f.lot_key, lk.to_lot_id, t.lot_key,
		lk.weight_g, lk.metal_mg, lk.share_ppm, lk.basis
		from rt.lot_links lk join rt.inventory_lots f on f.id=lk.from_lot_id
		join rt.inventory_lots t on t.id=lk.to_lot_id where lk.from_lot_id=$1 order by lk.id`, lotID)
}

func (a *App) LinksTo(ctx context.Context, lotID int64) ([]Link, error) {
	return a.queryLinks(ctx, `select lk.id, lk.movement_id, lk.from_lot_id, f.lot_key, lk.to_lot_id, t.lot_key,
		lk.weight_g, lk.metal_mg, lk.share_ppm, lk.basis
		from rt.lot_links lk join rt.inventory_lots f on f.id=lk.from_lot_id
		join rt.inventory_lots t on t.id=lk.to_lot_id where lk.to_lot_id=$1 order by lk.id`, lotID)
}

func (a *App) queryLinks(ctx context.Context, sql string, arg int64) ([]Link, error) {
	rows, err := a.DB.Pool.Query(ctx, sql, arg)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Link{}
	for rows.Next() {
		var l Link
		if err := rows.Scan(&l.ID, &l.MovementID, &l.FromLotID, &l.FromKey, &l.ToLotID, &l.ToKey,
			&l.WeightG, &l.MetalMg, &l.SharePPM, &l.Basis); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

type LotEvent struct {
	ID         int64  `json:"id"`
	LotID      int64  `json:"lot_id"`
	LotKey     string `json:"lot_key"`
	MovementID *int64 `json:"movement_id"`
	Event      string `json:"event"`
	WeightG    int64  `json:"weight_g"`
	MetalMg    int64  `json:"metal_mg"`
	PileName   string `json:"pile_name"`
	OccurredAt string `json:"occurred_at"`
	Detail     string `json:"detail"`
}

func (a *App) LotTimeline(ctx context.Context, lotID int64) ([]LotEvent, error) {
	rows, err := a.DB.Pool.Query(ctx, `select e.id, e.lot_id, l.lot_key, e.movement_id, e.event, e.weight_g, e.metal_mg,
		coalesce(p.name,''), to_char(e.occurred_at,'YYYY-MM-DD"T"HH24:MI:SSOF'), e.detail::text
		from rt.lot_events e join rt.inventory_lots l on l.id=e.lot_id left join rt.piles p on p.id=e.pile_id
		where e.lot_id=$1 order by e.occurred_at, e.id`, lotID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []LotEvent{}
	for rows.Next() {
		var e LotEvent
		if err := rows.Scan(&e.ID, &e.LotID, &e.LotKey, &e.MovementID, &e.Event, &e.WeightG, &e.MetalMg,
			&e.PileName, &e.OccurredAt, &e.Detail); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// TraceLot walks the link graph backwards to the source trips and forwards to
// the downstream lots still holding material. It never loops by pile name; a
// lot returning to the same pile appears as a new node.
type TraceNode struct {
	LotID    int64  `json:"lot_id"`
	LotKey   string `json:"lot_key"`
	PileName string `json:"pile_name"`
	WeightG  int64  `json:"weight_g"`
	MetalMg  int64  `json:"metal_mg"`
	Depth    int    `json:"depth"`
	Via      string `json:"via"`
}

type Trace struct {
	Lot          Lot         `json:"lot"`
	Upstream     []TraceNode `json:"upstream"`
	Downstream   []TraceNode `json:"downstream"`
	SourceTrips  []Origin    `json:"source_trips"`
	AffectedLots []TraceNode `json:"affected_lots"`
}

func (a *App) Trace(ctx context.Context, lotID int64) (Trace, error) {
	lot, err := a.GetLot(ctx, lotID)
	if err != nil {
		return Trace{}, err
	}
	upRows, err := a.DB.Pool.Query(ctx, `
		with recursive up(lot_id, depth) as (
			select $1::bigint, 0
			union
			select lk.from_lot_id, up.depth+1 from up join rt.lot_links lk on lk.to_lot_id=up.lot_id
			where up.depth < 20
		)
		select distinct on (up.lot_id) up.lot_id, up.depth, l.lot_key, p.name, l.weight_g, l.metal_mg
		from up join rt.inventory_lots l on l.id=up.lot_id join rt.piles p on p.id=l.pile_id
		where up.lot_id <> $1 order by up.lot_id, up.depth`, lotID)
	if err != nil {
		return Trace{}, err
	}
	up := []TraceNode{}
	for upRows.Next() {
		var n TraceNode
		if err := upRows.Scan(&n.LotID, &n.Depth, &n.LotKey, &n.PileName, &n.WeightG, &n.MetalMg); err != nil {
			upRows.Close()
			return Trace{}, err
		}
		n.Via = "upstream"
		up = append(up, n)
	}
	upRows.Close()

	downRows, err := a.DB.Pool.Query(ctx, `
		with recursive down(lot_id, depth) as (
			select $1::bigint, 0
			union
			select lk.to_lot_id, down.depth+1 from down join rt.lot_links lk on lk.from_lot_id=down.lot_id
			where down.depth < 20
		)
		select distinct on (down.lot_id) down.lot_id, down.depth, l.lot_key, p.name, l.weight_g, l.metal_mg
		from down join rt.inventory_lots l on l.id=down.lot_id join rt.piles p on p.id=l.pile_id
		where down.lot_id <> $1 order by down.lot_id, down.depth`, lotID)
	if err != nil {
		return Trace{}, err
	}
	down := []TraceNode{}
	for downRows.Next() {
		var n TraceNode
		if err := downRows.Scan(&n.LotID, &n.Depth, &n.LotKey, &n.PileName, &n.WeightG, &n.MetalMg); err != nil {
			downRows.Close()
			return Trace{}, err
		}
		n.Via = "downstream"
		down = append(down, n)
	}
	downRows.Close()

	// Source trips from this lot's origins plus any upstream lot's origins.
	seenTrip := map[int64]bool{}
	trips := []Origin{}
	addTrip := func(o Origin, basis string) {
		if seenTrip[o.TripID] {
			return
		}
		seenTrip[o.TripID] = true
		o.Basis = basis
		trips = append(trips, o)
	}
	for _, o := range lot.Origins {
		addTrip(o, o.Basis)
	}
	// Affected stock: lots still holding weight that are downstream and in
	// available/held state.
	affected := []TraceNode{}
	for _, n := range down {
		if n.WeightG > 0 {
			affected = append(affected, n)
		}
	}
	return Trace{Lot: lot, Upstream: up, Downstream: down, SourceTrips: trips, AffectedLots: affected}, nil
}

// TripsAffectedByLot finds every trip contributing to a lot and every downstream
// lot still holding stock, so a re-assayed trip can be located precisely.
func (a *App) TripsAffectedByTrip(ctx context.Context, tripID int64) ([]Lot, []Lot, error) {
	rows, err := a.DB.Pool.Query(ctx, `select lot_id from rt.lot_origins where trip_id=$1 order by lot_id`, tripID)
	if err != nil {
		return nil, nil, err
	}
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	direct := []Lot{}
	for _, id := range ids {
		l, err := a.GetLot(ctx, id)
		if err != nil {
			return nil, nil, err
		}
		direct = append(direct, l)
	}
	// Downstream of the earliest lot per pile (the trip's own lot).
	seen := map[int64]bool{}
	downstream := []Lot{}
	for _, l := range direct {
		var downIDs []int64
		drows, err := a.DB.Pool.Query(ctx, `
			with recursive down(lot_id) as (
				select $1::bigint
				union
				select lk.to_lot_id from down join rt.lot_links lk on lk.from_lot_id=down.lot_id
			) select lot_id from down where lot_id<>$1`, l.ID)
		if err != nil {
			return nil, nil, err
		}
		for drows.Next() {
			var id int64
			if err := drows.Scan(&id); err != nil {
				drows.Close()
				return nil, nil, err
			}
			downIDs = append(downIDs, id)
		}
		drows.Close()
		for _, id := range downIDs {
			if seen[id] {
				continue
			}
			seen[id] = true
			dl, err := a.GetLot(ctx, id)
			if err != nil {
				return nil, nil, err
			}
			downstream = append(downstream, dl)
		}
	}
	return direct, downstream, nil
}

// ------------------------------------------------------------------ movements

func (a *App) GetMovementByRef(ctx context.Context, ref string) (Movement, error) {
	var id int64
	err := a.DB.Pool.QueryRow(ctx, `select id from rt.movements where external_ref=$1`, ref).Scan(&id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Movement{}, notFound("move %q", ref)
		}
		return Movement{}, err
	}
	return a.GetMovementByID(ctx, id)
}

func (a *App) GetMovementByID(ctx context.Context, id int64) (Movement, error) {
	var m Movement
	err := a.DB.Pool.QueryRow(ctx, `select id, kind, external_ref,
		to_char(occurred_at,'YYYY-MM-DD"T"HH24:MI:SSOF'), actor_id, note,
		to_char(created_at,'YYYY-MM-DD"T"HH24:MI:SSOF') from rt.movements where id=$1`, id).
		Scan(&m.ID, &m.Kind, &m.ExternalRef, &m.OccurredAt, &m.ActorID, &m.Note, &m.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return m, notFound("movement %d", id)
		}
		return m, err
	}
	rows, err := a.DB.Pool.Query(ctx, `select tl.lot_id, l.lot_key, direction, tl.weight_g, tl.metal_mg,
		tl.basis, tl.loss_class, p.name
		from rt.transfer_lines tl join rt.inventory_lots l on l.id=tl.lot_id
		join rt.piles p on p.id=l.pile_id where tl.movement_id=$1 order by tl.id`, id)
	if err != nil {
		return m, err
	}
	defer rows.Close()
	m.Lines = []TransferLine{}
	for rows.Next() {
		var l TransferLine
		if err := rows.Scan(&l.LotID, &l.LotKey, &l.Direction, &l.WeightG, &l.MetalMg, &l.Basis, &l.LossClass, &l.PileName); err != nil {
			return m, err
		}
		m.Lines = append(m.Lines, l)
	}
	return m, rows.Err()
}

func (a *App) ListMovements(ctx context.Context) ([]Movement, error) {
	rows, err := a.DB.Pool.Query(ctx, `select id from rt.movements order by id desc limit 200`)
	if err != nil {
		return nil, err
	}
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	out := []Movement{}
	for _, id := range ids {
		m, err := a.GetMovementByID(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, nil
}

// Reconciliation is a full conservation statement across inventory, links and
// payables. Totals alone never stand in for per-trip traceability; this report
// is presented per trip and per pile.
type TripRecon struct {
	Trips  []TripConservation `json:"trips"`
	Piles  []PileConservation `json:"piles"`
	Totals ConservationTotals `json:"totals"`
}

type TripConservation struct {
	TripID                int64  `json:"trip_id"`
	BatchNo               string `json:"batch_no"`
	IntakeG               int64  `json:"intake_g"`
	StockG                int64  `json:"stock_g"`
	ProductionG           int64  `json:"production_g"`
	ShippedG              int64  `json:"shipped_g"`
	ProcessLossG          int64  `json:"process_loss_g"`
	DryingLossG           int64  `json:"drying_loss_g"`
	OnHandG               int64  `json:"on_hand_g"`
	IntakeMetalMg         int64  `json:"intake_metal_mg"`
	OriginalIntakeMetalMg int64  `json:"original_intake_metal_mg"`
	MetalDeltaMg          int64  `json:"metal_delta_mg"`
	StockMetalMg          int64  `json:"stock_metal_mg"`
	ShippedMetalMg        int64  `json:"shipped_metal_mg"`
	LossMetalMg           int64  `json:"loss_metal_mg"`
	OnHandMetalMg         int64  `json:"on_hand_metal_mg"`
	MetalBalanced         bool   `json:"metal_balanced"`
	WeightBalanced        bool   `json:"weight_balanced"`
	PayableCents          int64  `json:"payable_cents"`
	PaidCents             int64  `json:"paid_cents"`
}

type PileConservation struct {
	PileID   int64  `json:"pile_id"`
	PileName string `json:"pile_name"`
	PileKind string `json:"pile_kind"`
	WeightG  int64  `json:"weight_g"`
	MetalMg  int64  `json:"metal_mg"`
}

type ConservationTotals struct {
	IntakeG           int64 `json:"intake_g"`
	OnHandG           int64 `json:"on_hand_g"`
	ProductionG       int64 `json:"production_g"`
	ProductionMetalMg int64 `json:"production_metal_mg"`
	MetalDeltaMg      int64 `json:"metal_delta_mg"`
	ShippedG          int64 `json:"shipped_g"`
	LossG             int64 `json:"loss_g"`
	GapG              int64 `json:"gap_g"`
	IntakeMetalMg     int64 `json:"intake_metal_mg"`
	OnHandMetalMg     int64 `json:"on_hand_metal_mg"`
	ShippedMetalMg    int64 `json:"shipped_metal_mg"`
	LossMetalMg       int64 `json:"loss_metal_mg"`
	GapMetalMg        int64 `json:"gap_metal_mg"`
}

func (a *App) Reconcile(ctx context.Context) (TripRecon, error) {
	out := TripRecon{}
	states, err := a.StockByPile(ctx)
	if err != nil {
		return out, err
	}
	for _, st := range states {
		out.Piles = append(out.Piles, PileConservation{st.Pile.ID, st.Pile.Name, st.Pile.Kind, st.WeightG, st.MetalMg})
		// "On hand" means storage piles only; production/shipped/loss piles are
		// separate legs of the conservation identity and must not double count.
		switch st.Pile.Kind {
		case "receiving", "usable", "isolated":
			out.Totals.OnHandG += st.WeightG
			out.Totals.OnHandMetalMg += st.MetalMg
		case "production":
			out.Totals.ProductionG += st.WeightG
			out.Totals.ProductionMetalMg += st.MetalMg
		case "shipped":
			out.Totals.ShippedG += st.WeightG
			out.Totals.ShippedMetalMg += st.MetalMg
		case "loss":
			out.Totals.LossG += st.WeightG
			out.Totals.LossMetalMg += st.MetalMg
		}
	}
	trows, err := a.DB.Pool.Query(ctx, `select id, batch_no from rt.trips order by id`)
	if err != nil {
		return out, err
	}
	type tb struct {
		id    int64
		batch string
	}
	trips := []tb{}
	for trows.Next() {
		var t tb
		if err := trows.Scan(&t.id, &t.batch); err != nil {
			trows.Close()
			return out, err
		}
		trips = append(trips, t)
	}
	trows.Close()

	for _, t := range trips {
		tc := TripConservation{TripID: t.id, BatchNo: t.batch}
		// Intake only counts once material has actually been placed. A trip with
		// adopted weights but no intake lot and no origin is reported as zero
		// intake rather than as a phantom gap.
		var intaked bool
		if err := a.DB.Pool.QueryRow(ctx, `select exists(select 1 from rt.inventory_lots where lot_key=$1)
			or exists(select 1 from rt.lot_origins where trip_id=$2)`,
			fmt.Sprintf("TRIP-%d", t.id), t.id).Scan(&intaked); err != nil {
			return out, err
		}
		if intaked {
			_ = a.DB.Pool.QueryRow(ctx, `select net_g from rt.weight_adoptions where trip_id=$1 and is_current`, t.id).Scan(&tc.IntakeG)
		}
		// On hand: origins of lots still holding stock, excluding production/shipped/loss piles.
		_ = a.DB.Pool.QueryRow(ctx, `select coalesce(sum(o.weight_g),0), coalesce(sum(o.metal_mg),0)
			from rt.lot_origins o join rt.inventory_lots l on l.id=o.lot_id join rt.piles p on p.id=l.pile_id
			where o.trip_id=$1 and p.kind in ('receiving','usable','isolated')`, t.id).
			Scan(&tc.OnHandG, &tc.OnHandMetalMg)
		// Production in progress: material already at the furnace.
		var prodG, prodM int64
		_ = a.DB.Pool.QueryRow(ctx, `select coalesce(sum(o.weight_g),0), coalesce(sum(o.metal_mg),0)
			from rt.lot_origins o join rt.inventory_lots l on l.id=o.lot_id join rt.piles p on p.id=l.pile_id
			where o.trip_id=$1 and p.kind='production'`, t.id).Scan(&prodG, &prodM)
		tc.ProductionG = prodG
		// Shipped.
		_ = a.DB.Pool.QueryRow(ctx, `select coalesce(sum(o.weight_g),0), coalesce(sum(o.metal_mg),0)
			from rt.lot_origins o join rt.inventory_lots l on l.id=o.lot_id join rt.piles p on p.id=l.pile_id
			where o.trip_id=$1 and p.kind='shipped'`, t.id).Scan(&tc.ShippedG, &tc.ShippedMetalMg)
		// Losses from the attribution ledger: drying and process kept apart.
		// FILTER must wrap the aggregate, not a coalesce of it.
		if err := a.DB.Pool.QueryRow(ctx, `select
			coalesce(sum(weight_g) filter (where loss_class='process'),0),
			coalesce(sum(weight_g) filter (where loss_class='drying'),0),
			coalesce(sum(metal_mg),0)
			from rt.loss_attribution where trip_id=$1`, t.id).
			Scan(&tc.ProcessLossG, &tc.DryingLossG, &tc.LossMetalMg); err != nil {
			return out, err
		}
		// Recognised intake metal is the CURRENT contract result, because a
		// quality re-test corrects the trip's metal. The original payable metal
		// is reported separately as the delta already booked.
		if intaked {
			if cur, err := computeTripMetal(ctx, a.DB.Pool, t.id); err == nil {
				tc.IntakeMetalMg = cur.MetalMg
			}
		}
		var firstPayableMetal int64
		var hasPayable bool
		if err := a.DB.Pool.QueryRow(ctx, `select metal_mg from rt.payables where trip_id=$1 order by version limit 1`, t.id).
			Scan(&firstPayableMetal); err == nil {
			hasPayable = true
		}
		tc.OriginalIntakeMetalMg = firstPayableMetal
		if hasPayable {
			tc.MetalDeltaMg = tc.IntakeMetalMg - firstPayableMetal
		}
		tc.StockG = tc.IntakeG
		tc.WeightBalanced = tc.IntakeG == tc.OnHandG+tc.ProductionG+tc.ShippedG+tc.ProcessLossG+tc.DryingLossG
		tc.MetalBalanced = tc.IntakeMetalMg == tc.OnHandMetalMg+prodM+tc.ShippedMetalMg+tc.LossMetalMg
		// Payables.
		_ = a.DB.Pool.QueryRow(ctx, `select coalesce(sum(amount_cents),0) from rt.payables where trip_id=$1 and is_current
			and status in ('calculated','approved','paid')`, t.id).Scan(&tc.PayableCents)
		_ = a.DB.Pool.QueryRow(ctx, `select coalesce(sum(amount_cents),0) from rt.payables where trip_id=$1 and status='paid'`, t.id).Scan(&tc.PaidCents)
		out.Totals.IntakeG += tc.IntakeG
		out.Totals.IntakeMetalMg += tc.IntakeMetalMg
		out.Totals.MetalDeltaMg += tc.MetalDeltaMg
		out.Trips = append(out.Trips, tc)
	}
	out.Totals.GapG = out.Totals.IntakeG - out.Totals.OnHandG - out.Totals.ProductionG - out.Totals.ShippedG - out.Totals.LossG
	out.Totals.GapMetalMg = out.Totals.IntakeMetalMg - out.Totals.OnHandMetalMg - out.Totals.ProductionMetalMg - out.Totals.ShippedMetalMg - out.Totals.LossMetalMg
	return out, nil
}
