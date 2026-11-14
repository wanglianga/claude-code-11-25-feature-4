package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"recycletrace/backend/internal/num"
)

var (
	ErrNotFound   = errors.New("not found")
	ErrConflict   = errors.New("conflict")
	ErrValidation = errors.New("validation failed")
	ErrBlocked    = errors.New("blocked")
)

func conflict(format string, a ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrConflict}, a...)...)
}

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrValidation}, a...)...)
}

func blocked(format string, a ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrBlocked}, a...)...)
}

func notFound(format string, a ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrNotFound}, a...)...)
}

// ---------------------------------------------------------------------------
// Master data
// ---------------------------------------------------------------------------

type Supplier struct {
	ID   int64  `json:"id"`
	Code string `json:"code"`
	Name string `json:"name"`
}

type Grade struct {
	Code          string `json:"grade_code"`
	CoefPPM       int64  `json:"coef_ppm"`
	IntakeAllowed bool   `json:"intake_allowed"`
}

type Contract struct {
	ID                 int64       `json:"id"`
	Code               string      `json:"code"`
	SupplierID         int64       `json:"supplier_id"`
	Name               string      `json:"name"`
	FormulaCode        string      `json:"formula_code"`
	Formula            num.Formula `json:"formula"`
	MetalBasis         string      `json:"metal_basis"`
	UnitPriceCentsPerT int64       `json:"unit_price_cents_per_t"`
	Active             bool        `json:"active"`
	Grades             []Grade     `json:"grades"`
	Note               string      `json:"note"`
}

type Pile struct {
	ID       int64  `json:"id"`
	Code     string `json:"code"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	Location string `json:"physical_location"`
}

func (a *App) ListSuppliers(ctx context.Context) ([]Supplier, error) {
	rows, err := a.DB.Pool.Query(ctx, `select id, code, name from rt.suppliers order by id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Supplier{}
	for rows.Next() {
		var s Supplier
		if err := rows.Scan(&s.ID, &s.Code, &s.Name); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (a *App) ListPiles(ctx context.Context) ([]Pile, error) {
	rows, err := a.DB.Pool.Query(ctx, `select id, code, name, kind, physical_location from rt.piles order by id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Pile{}
	for rows.Next() {
		var p Pile
		if err := rows.Scan(&p.ID, &p.Code, &p.Name, &p.Kind, &p.Location); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (a *App) GetContract(ctx context.Context, id int64) (Contract, error) {
	var c Contract
	c.Grades = []Grade{}
	var formulaCode string
	err := a.DB.Pool.QueryRow(ctx, `select id, code, supplier_id, name, metal_basis, unit_price_cents_per_t,
		active, note, coalesce(formula->>'code','') from rt.contracts where id=$1`, id).
		Scan(&c.ID, &c.Code, &c.SupplierID, &c.Name, &c.MetalBasis, &c.UnitPriceCentsPerT, &c.Active, &c.Note, &formulaCode)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return c, notFound("contract %d", id)
		}
		return c, err
	}
	c.FormulaCode = formulaCode
	if f, ok := num.Formulas[formulaCode]; ok {
		c.Formula = f
	}
	rows, err := a.DB.Pool.Query(ctx, `select grade_code, coef_ppm, intake_allowed from rt.contract_grades where contract_id=$1 order by grade_code`, id)
	if err != nil {
		return c, err
	}
	defer rows.Close()
	for rows.Next() {
		var g Grade
		if err := rows.Scan(&g.Code, &g.CoefPPM, &g.IntakeAllowed); err != nil {
			return c, err
		}
		c.Grades = append(c.Grades, g)
	}
	return c, rows.Err()
}

func (a *App) ListContracts(ctx context.Context) ([]Contract, error) {
	rows, err := a.DB.Pool.Query(ctx, `select id from rt.contracts where active order by id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Contract{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		c, err := a.GetContract(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// Trips, weighings, adoption
// ---------------------------------------------------------------------------

type Trip struct {
	ID           int64     `json:"id"`
	BatchNo      string    `json:"batch_no"`
	SupplierID   int64     `json:"supplier_id"`
	SupplierName string    `json:"supplier_name"`
	ContractID   int64     `json:"contract_id"`
	ContractCode string    `json:"contract_code"`
	PlateNo      string    `json:"plate_no"`
	Driver       string    `json:"driver"`
	ArrivalAt    time.Time `json:"arrival_at"`
	Note         string    `json:"note"`
	CreatedAt    time.Time `json:"created_at"`
}

type Weighing struct {
	ID          int64     `json:"id"`
	TripID      int64     `json:"trip_id"`
	Kind        string    `json:"kind"`
	Seq         int       `json:"seq"`
	WeightG     int64     `json:"weight_g"`
	WeighedAt   time.Time `json:"weighed_at"`
	ScaleDevice string    `json:"scale_device"`
	TicketNo    string    `json:"ticket_no"`
	Source      string    `json:"source"`
	IsVoid      bool      `json:"is_void"`
	VoidReason  string    `json:"void_reason"`
	CreatedAt   time.Time `json:"created_at"`
}

type Adoption struct {
	ID              int64     `json:"id"`
	TripID          int64     `json:"trip_id"`
	GrossWeighingID int64     `json:"gross_weighing_id"`
	TareWeighingID  int64     `json:"tare_weighing_id"`
	GrossWeightG    int64     `json:"gross_weight_g"`
	TareWeightG     int64     `json:"tare_weight_g"`
	NetG            int64     `json:"net_g"`
	BasisNote       string    `json:"basis_note"`
	AdoptedBy       int64     `json:"adopted_by"`
	AdoptedAt       time.Time `json:"adopted_at"`
	IsCurrent       bool      `json:"is_current"`
	SupersedesID    *int64    `json:"supersedes_id"`
}

type TripDetail struct {
	Trip      Trip           `json:"trip"`
	Weighings []Weighing     `json:"weighings"`
	Adoption  *Adoption      `json:"adoption"`
	Samples   []Sample       `json:"samples"`
	Assays    []Assay        `json:"assays"`
	Grade     *GradeApproval `json:"grade"`
	Lots      []Lot          `json:"lots"`
	Payable   *Payable       `json:"payable"`
}

type CreateTripInput struct {
	SupplierID int64     `json:"supplier_id"`
	ContractID int64     `json:"contract_id"`
	PlateNo    string    `json:"plate_no"`
	Driver     string    `json:"driver"`
	ArrivalAt  time.Time `json:"arrival_at"`
	Note       string    `json:"note"`
}

// CreateTrip establishes a stable per-truck batch. Same supplier + same day
// still produces distinct batches, so an assay can never be assigned to a trip
// merely because the supplier and date match.
func (a *App) CreateTrip(ctx context.Context, actor User, in CreateTripInput) (Trip, error) {
	if in.SupplierID == 0 || in.ContractID == 0 {
		return Trip{}, invalid("supplier_id and contract_id are required")
	}
	if in.ArrivalAt.IsZero() {
		in.ArrivalAt = time.Now()
	}
	var batchNo string
	err := a.DB.Tx(ctx, func(tx pgx.Tx) error {
		var supplierExists bool
		if err := tx.QueryRow(ctx, `select exists(select 1 from rt.suppliers where id=$1)`, in.SupplierID).Scan(&supplierExists); err != nil {
			return err
		}
		if !supplierExists {
			return invalid("unknown supplier")
		}
		var contractSupplier int64
		var active bool
		if err := tx.QueryRow(ctx, `select supplier_id, active from rt.contracts where id=$1`, in.ContractID).
			Scan(&contractSupplier, &active); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return invalid("unknown contract")
			}
			return err
		}
		if contractSupplier != in.SupplierID {
			return invalid("contract does not belong to the supplier")
		}
		if !active {
			return invalid("contract is not active")
		}
		// Per-day sequence taken under a lock so two concurrent creates never
		// produce the same batch number.
		day := in.ArrivalAt.Format("20060102")
		if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtext('trip-batch-'||$1))`, day); err != nil {
			return err
		}
		var n int
		if err := tx.QueryRow(ctx, `select count(*)+1 from rt.trips where batch_no like $1`,
			"B-"+day+"-%").Scan(&n); err != nil {
			return err
		}
		batchNo = fmt.Sprintf("B-%s-%03d", day, n)
		var id int64
		err := tx.QueryRow(ctx, `insert into rt.trips(batch_no, supplier_id, contract_id, plate_no, driver, arrival_at, note)
			values($1,$2,$3,$4,$5,$6,$7) returning id`,
			batchNo, in.SupplierID, in.ContractID, in.PlateNo, in.Driver, in.ArrivalAt, in.Note).Scan(&id)
		if err != nil {
			return err
		}
		return insertAudit(ctx, tx, actor, "trip.create", "trip", id, map[string]any{"batch_no": batchNo})
	})
	if err != nil {
		return Trip{}, err
	}
	return a.GetTrip(ctx, 0, batchNo)
}

func (a *App) GetTrip(ctx context.Context, id int64, batchNo string) (Trip, error) {
	q := `select t.id, t.batch_no, t.supplier_id, s.name, t.contract_id, c.code, t.plate_no, t.driver,
		t.arrival_at, t.note, t.created_at
		from rt.trips t join rt.suppliers s on s.id=t.supplier_id join rt.contracts c on c.id=t.contract_id`
	var t Trip
	var err error
	if id > 0 {
		err = a.DB.Pool.QueryRow(ctx, q+` where t.id=$1`, id).Scan(&t.ID, &t.BatchNo, &t.SupplierID, &t.SupplierName,
			&t.ContractID, &t.ContractCode, &t.PlateNo, &t.Driver, &t.ArrivalAt, &t.Note, &t.CreatedAt)
	} else {
		err = a.DB.Pool.QueryRow(ctx, q+` where t.batch_no=$1`, batchNo).Scan(&t.ID, &t.BatchNo, &t.SupplierID, &t.SupplierName,
			&t.ContractID, &t.ContractCode, &t.PlateNo, &t.Driver, &t.ArrivalAt, &t.Note, &t.CreatedAt)
	}
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return t, notFound("trip not found")
		}
		return t, err
	}
	return t, nil
}

func (a *App) ListTrips(ctx context.Context) ([]Trip, error) {
	rows, err := a.DB.Pool.Query(ctx, `select t.id from rt.trips t order by t.arrival_at desc, t.id desc`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Trip{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		t, err := a.GetTrip(ctx, id, "")
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// AddWeighing records a scale-report weighing for a trip. Gross and tare are
// both kept; adopting one gross and one tare is a separate, audited step.
func (a *App) AddWeighing(ctx context.Context, actor User, tripID int64, in Weighing) (Weighing, error) {
	if in.Kind != "gross" && in.Kind != "tare" {
		return Weighing{}, invalid("kind must be gross or tare")
	}
	if in.WeightG <= 0 {
		return Weighing{}, invalid("weight_g must be positive")
	}
	if in.TicketNo == "" {
		return Weighing{}, invalid("ticket_no is required")
	}
	if in.WeighedAt.IsZero() {
		in.WeighedAt = time.Now()
	}
	if in.ScaleDevice == "" {
		in.ScaleDevice = "SCALE-LOCAL"
	}
	if in.Source == "" {
		in.Source = "scale-report"
	}
	var out Weighing
	err := a.DB.Tx(ctx, func(tx pgx.Tx) error {
		var tripExists bool
		if err := tx.QueryRow(ctx, `select exists(select 1 from rt.trips where id=$1)`, tripID).Scan(&tripExists); err != nil {
			return err
		}
		if !tripExists {
			return notFound("trip %d", tripID)
		}
		var seq int
		if err := tx.QueryRow(ctx, `select coalesce(max(seq),0)+1 from rt.weighings where trip_id=$1 and kind=$2`,
			tripID, in.Kind).Scan(&seq); err != nil {
			return err
		}
		err := tx.QueryRow(ctx, `insert into rt.weighings(trip_id, kind, seq, weight_g, weighed_at, scale_device, ticket_no, source)
			values($1,$2,$3,$4,$5,$6,$7,$8)
			returning id, trip_id, kind, seq, weight_g, weighed_at, scale_device, ticket_no, source, is_void, void_reason, created_at`,
			tripID, in.Kind, seq, in.WeightG, in.WeighedAt, in.ScaleDevice, in.TicketNo, in.Source).
			Scan(&out.ID, &out.TripID, &out.Kind, &out.Seq, &out.WeightG, &out.WeighedAt, &out.ScaleDevice,
				&out.TicketNo, &out.Source, &out.IsVoid, &out.VoidReason, &out.CreatedAt)
		if err != nil {
			return conflict("scale ticket %q already recorded", in.TicketNo)
		}
		return insertAudit(ctx, tx, actor, "weighing.create", "weighing", out.ID, map[string]any{
			"trip_id": tripID, "kind": in.Kind, "weight_g": in.WeightG, "ticket_no": in.TicketNo})
	})
	return out, err
}

// AdoptWeights chooses the effective gross and tare. Earlier adoptions stay
// intact for audit; the new one becomes current and records the basis.
func (a *App) AdoptWeights(ctx context.Context, actor User, tripID, grossID, tareID int64, basisNote string) (Adoption, error) {
	if err := RequireRole(actor, "yard", "purchasing"); err != nil {
		return Adoption{}, err
	}
	if basisNote == "" {
		return Adoption{}, invalid("basis_note is required to explain why this pair is adopted")
	}
	var out Adoption
	err := a.DB.Tx(ctx, func(tx pgx.Tx) error {
		var grossW, tareW int64
		var grossVoid, tareVoid bool
		err := tx.QueryRow(ctx, `select weight_g, is_void from rt.weighings where id=$1 and trip_id=$2 and kind='gross'`,
			grossID, tripID).Scan(&grossW, &grossVoid)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return invalid("gross weighing does not belong to this trip")
			}
			return err
		}
		err = tx.QueryRow(ctx, `select weight_g, is_void from rt.weighings where id=$1 and trip_id=$2 and kind='tare'`,
			tareID, tripID).Scan(&tareW, &tareVoid)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return invalid("tare weighing does not belong to this trip")
			}
			return err
		}
		if grossVoid || tareVoid {
			return invalid("cannot adopt a voided weighing")
		}
		net := grossW - tareW
		if net <= 0 {
			return invalid("gross (%d g) must exceed tare (%d g)", grossW, tareW)
		}
		var prevID *int64
		var prevNet int64
		if err := tx.QueryRow(ctx, `select id, net_g from rt.weight_adoptions where trip_id=$1 and is_current`, tripID).
			Scan(&prevID, &prevNet); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if prevID != nil {
			if _, err := tx.Exec(ctx, `update rt.weight_adoptions set is_current=false where id=$1`, *prevID); err != nil {
				return err
			}
		}
		note := basisNote
		if prevID != nil {
			note = fmt.Sprintf("%s（原毛重/皮重净重 %d g 保留原值）", basisNote, prevNet)
		}
		err = tx.QueryRow(ctx, `insert into rt.weight_adoptions(trip_id, gross_weighing_id, tare_weighing_id, net_g,
			basis_note, adopted_by, is_current, supersedes_id) values($1,$2,$3,$4,$5,$6,true,$7)
			returning id, trip_id, gross_weighing_id, tare_weighing_id, net_g, basis_note, adopted_by, adopted_at, is_current, supersedes_id`,
			tripID, grossID, tareID, net, note, actor.ID, prevID).
			Scan(&out.ID, &out.TripID, &out.GrossWeighingID, &out.TareWeighingID, &out.NetG, &out.BasisNote,
				&out.AdoptedBy, &out.AdoptedAt, &out.IsCurrent, &out.SupersedesID)
		if err != nil {
			return err
		}
		out.GrossWeightG = grossW
		out.TareWeightG = tareW
		return insertAudit(ctx, tx, actor, "weight.adopt", "trip", tripID, map[string]any{
			"adoption_id": out.ID, "net_g": net, "gross_id": grossID, "tare_id": tareID})
	})
	return out, err
}

func currentAdoption(ctx context.Context, q querier, tripID int64) (Adoption, error) {
	var out Adoption
	var grossW, tareW int64
	err := q.QueryRow(ctx, `select wa.id, wa.trip_id, wa.gross_weighing_id, wa.tare_weighing_id, wa.net_g,
		wa.basis_note, wa.adopted_by, wa.adopted_at, wa.is_current, wa.supersedes_id, g.weight_g, t.weight_g
		from rt.weight_adoptions wa
		join rt.weighings g on g.id=wa.gross_weighing_id
		join rt.weighings t on t.id=wa.tare_weighing_id
		where wa.trip_id=$1 and wa.is_current`, tripID).
		Scan(&out.ID, &out.TripID, &out.GrossWeighingID, &out.TareWeighingID, &out.NetG, &out.BasisNote,
			&out.AdoptedBy, &out.AdoptedAt, &out.IsCurrent, &out.SupersedesID, &grossW, &tareW)
	out.GrossWeightG = grossW
	out.TareWeightG = tareW
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return out, blocked("no adopted gross/tare pair for trip %d", tripID)
		}
		return out, err
	}
	return out, nil
}

type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

func insertAudit(ctx context.Context, tx pgx.Tx, actor User, action, entity string, entityID any, detail map[string]any) error {
	_, err := tx.Exec(ctx, `insert into rt.audit_log(actor_id, action, entity, entity_id, detail) values($1,$2,$3,$4,$5)`,
		actor.ID, action, entity, fmt.Sprint(entityID), detail)
	return err
}

// TripDetail assembles the full dossier of one truck: weighings, current
// adoption, sealed samples, assay history, grade, its lots and current payable.
func (a *App) TripDetail(ctx context.Context, tripID int64) (TripDetail, error) {
	out := TripDetail{}
	t, err := a.GetTrip(ctx, tripID, "")
	if err != nil {
		return out, err
	}
	out.Trip = t
	rows, err := a.DB.Pool.Query(ctx, `select id, trip_id, kind, seq, weight_g, weighed_at, scale_device, ticket_no, source,
		is_void, void_reason, created_at from rt.weighings where trip_id=$1 order by kind, seq`, tripID)
	if err != nil {
		return out, err
	}
	out.Weighings = []Weighing{}
	for rows.Next() {
		var w Weighing
		if err := rows.Scan(&w.ID, &w.TripID, &w.Kind, &w.Seq, &w.WeightG, &w.WeighedAt, &w.ScaleDevice,
			&w.TicketNo, &w.Source, &w.IsVoid, &w.VoidReason, &w.CreatedAt); err != nil {
			rows.Close()
			return out, err
		}
		out.Weighings = append(out.Weighings, w)
	}
	rows.Close()

	var ad appAdoption
	err = a.DB.Pool.QueryRow(ctx, `select wa.id, wa.net_g, wa.basis_note, wa.adopted_at,
		(select weight_g from rt.weighings where id=wa.gross_weighing_id),
		(select weight_g from rt.weighings where id=wa.tare_weighing_id),
		wa.gross_weighing_id, wa.tare_weighing_id
		from rt.weight_adoptions wa where wa.trip_id=$1 and wa.is_current`, tripID).
		Scan(&ad.id, &ad.net, &ad.basis, &ad.at, &ad.gross, &ad.tare, &ad.grossID, &ad.tareID)
	if err == nil {
		out.Adoption = &Adoption{ID: ad.id, TripID: tripID, NetG: ad.net, BasisNote: ad.basis, AdoptedAt: ad.at,
			GrossWeightG: ad.gross, TareWeightG: ad.tare, GrossWeighingID: ad.grossID, TareWeighingID: ad.tareID, IsCurrent: true}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return out, err
	}

	samples, assays, err := a.ListSamplesForTrip(ctx, tripID)
	if err != nil {
		return out, err
	}
	out.Samples = samples
	out.Assays = assays

	var g GradeApproval
	var apBy *int64
	var apAt *time.Time
	err = a.DB.Pool.QueryRow(ctx, `select id, trip_id, grade_code, coef_ppm, isolated, status, approved_by, approved_at, note
		from rt.grade_approvals where trip_id=$1`, tripID).
		Scan(&g.ID, &g.TripID, &g.GradeCode, &g.CoefPPM, &g.Isolated, &g.Status, &apBy, &apAt, &g.Note)
	if err == nil {
		g.ApprovedBy = apBy
		g.ApprovedAt = apAt
		out.Grade = &g
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return out, err
	}

	// Lots currently holding this trip's material: the trip's own lot plus every
	// downstream node whose origin ledger still names this trip.
	rows, err = a.DB.Pool.Query(ctx, `select l.id from rt.inventory_lots l
		where l.trip_id=$1
		   or exists(select 1 from rt.lot_origins o where o.lot_id=l.id and o.trip_id=$1 and o.weight_g > 0)
		order by l.id`, tripID)
	if err != nil {
		return out, err
	}
	lids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return out, err
		}
		lids = append(lids, id)
	}
	rows.Close()
	out.Lots = []Lot{}
	for _, id := range lids {
		l, err := a.GetLot(ctx, id)
		if err != nil {
			return out, err
		}
		// Keep a fully consumed original lot visible as history but only when it
		// still has a balance; downstream live state is reached via the trace.
		if l.WeightG == 0 && l.State == "consumed" {
			continue
		}
		out.Lots = append(out.Lots, l)
	}

	var payID int64
	err = a.DB.Pool.QueryRow(ctx, `select id from rt.payables where trip_id=$1 and is_current`, tripID).Scan(&payID)
	if err == nil {
		p, err := a.GetPayable(ctx, payID)
		if err != nil {
			return out, err
		}
		out.Payable = &p
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return out, err
	}
	return out, nil
}

type appAdoption struct {
	id, net, gross, tare, grossID, tareID int64
	basis                                 string
	at                                    time.Time
}

// PendingIntake lists trips that have an adopted weight, an effective assay and
// an approved grade but whose net material has not yet been placed in a pile.
func (a *App) PendingIntake(ctx context.Context) ([]TripDetail, error) {
	rows, err := a.DB.Pool.Query(ctx, `
		select t.id from rt.trips t
		where exists(select 1 from rt.weight_adoptions wa where wa.trip_id=t.id and wa.is_current)
		  and exists(select 1 from rt.trip_assay_adoptions a where a.trip_id=t.id)
		  and exists(select 1 from rt.grade_approvals g where g.trip_id=t.id and g.status='approved')
		  and not exists(select 1 from rt.inventory_lots l where l.trip_id=t.id)
		order by t.arrival_at desc`)
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
	out := []TripDetail{}
	for _, id := range ids {
		d, err := a.TripDetail(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, nil
}
