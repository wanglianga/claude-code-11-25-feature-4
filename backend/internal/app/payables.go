package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"recycletrace/backend/internal/num"
)

type Payable struct {
	ID               int64           `json:"id"`
	TripID           int64           `json:"trip_id"`
	BatchNo          string          `json:"batch_no"`
	Version          int             `json:"version"`
	IsCurrent        bool            `json:"is_current"`
	SupersedesID     *int64          `json:"supersedes_id"`
	WetNetG          int64           `json:"wet_net_g"`
	DryG             int64           `json:"dry_g"`
	MoistureG        int64           `json:"moisture_g"`
	ImpurityG        int64           `json:"impurity_g"`
	PricedG          int64           `json:"priced_g"`
	MetalMg          int64           `json:"metal_mg"`
	CoefPPM          int64           `json:"coef_ppm"`
	UnitPriceCPT     int64           `json:"unit_price_cents_per_t"`
	AmountCents      int64           `json:"amount_cents"`
	FormulaCode      string          `json:"formula_code"`
	Formula          num.Formula     `json:"formula"`
	FormulaSnapshot  json.RawMessage `json:"formula_snapshot"`
	Status           string          `json:"status"`
	HoldReason       string          `json:"hold_reason"`
	WeightAdoptionID *int64          `json:"weight_adoption_id"`
	AssayID          *int64          `json:"assay_id"`
	GradeApprovalID  *int64          `json:"grade_approval_id"`
	ContractID       *int64          `json:"contract_id"`
	ComputedAt       string          `json:"computed_at"`
	ApprovedAt       *string         `json:"approved_at"`
	Note             string          `json:"note"`
	Adjustments      []Adjustment    `json:"adjustments"`
	Frozen           bool            `json:"frozen"`
	FreezeReason     string          `json:"freeze_reason,omitempty"`
}

type Adjustment struct {
	ID         int64  `json:"id"`
	DeltaCents int64  `json:"delta_cents"`
	OldWetNetG int64  `json:"old_wet_net_g"`
	NewWetNetG int64  `json:"new_wet_net_g"`
	OldMetalMg int64  `json:"old_metal_mg"`
	NewMetalMg int64  `json:"new_metal_mg"`
	Reason     string `json:"reason"`
	CreatedAt  string `json:"created_at"`
}

type CalcPreview struct {
	TripID        int64      `json:"trip_id"`
	BatchNo       string     `json:"batch_no"`
	FormulaCode   string     `json:"formula_code"`
	Result        num.Result `json:"result"`
	Warnings      []string   `json:"warnings"`
	CanPay        bool       `json:"can_pay"`
	BlockedReason string     `json:"blocked_reason"`
	ContractID    int64      `json:"contract_id"`
	AdoptionID    int64      `json:"weight_adoption_id"`
	AssayID       int64      `json:"assay_id"`
	GradeID       int64      `json:"grade_approval_id"`
}

// priceTrip computes a trip's payable without persisting it. It refuses to
// compute when a required parameter is missing, so finance cannot pay on a gap.
func (a *App) priceTrip(ctx context.Context, q querier, tripID int64) (CalcPreview, error) {
	return a.priceTripWith(ctx, q, tripID, nil)
}

// priceTripWith is priceTrip with an optional assay override. Adjudication uses
// it to trial what a specific side's result would pay without adopting it yet.
func (a *App) priceTripWith(ctx context.Context, q querier, tripID int64, override *num.AssayInput) (CalcPreview, error) {
	var p CalcPreview
	trip, err := a.GetTrip(ctx, tripID, "")
	if err != nil {
		return p, err
	}
	p.TripID = trip.ID
	p.BatchNo = trip.BatchNo
	contract, err := a.GetContract(ctx, trip.ContractID)
	if err != nil {
		return p, err
	}
	p.ContractID = contract.ID
	p.FormulaCode = contract.FormulaCode

	adoption, err := currentAdoption(ctx, q, tripID)
	if err != nil {
		p.BlockedReason = err.Error()
		p.Warnings = append(p.Warnings, "缺少有效称次采用记录，暂停付款")
		return p, nil
	}
	p.AdoptionID = adoption.ID
	assay, err := effectiveAssay(ctx, q, tripID)
	if err != nil {
		p.BlockedReason = err.Error()
		p.Warnings = append(p.Warnings, "化验结果未经质量确认采用，暂停付款")
		return p, nil
	}
	p.AssayID = assay.ID

	var gradeID int64
	var gradeCode string
	var coef int64
	var isolated bool
	err = q.QueryRow(ctx, `select id, grade_code, coef_ppm, isolated from rt.grade_approvals
		where trip_id=$1 and status='approved'`, tripID).Scan(&gradeID, &gradeCode, &coef, &isolated)
	if err != nil {
		p.BlockedReason = "车次尚无已批准等级，暂停付款"
		p.Warnings = append(p.Warnings, p.BlockedReason)
		return p, nil
	}
	p.GradeID = gradeID
	if isolated {
		p.Warnings = append(p.Warnings, fmt.Sprintf("等级 %s 为隔离料，仍可按合同计价但不进入可用堆", gradeCode))
	}

	in := num.Inputs{
		WetNetG: adoption.NetG,
		Assay: num.AssayInput{
			MoisturePPM:   assay.MoisturePPM,
			ImpurityPPM:   assay.ImpurityPPM,
			ImpurityBasis: assay.ImpurityBasis,
			MetalPPM:      assay.MetalPPM,
			MetalBasis:    assay.MetalBasis,
		},
		CoefPPM:      coef,
		UnitPriceCPT: contract.UnitPriceCentsPerT,
	}
	if override != nil {
		in.Assay = *override
	}
	res, err := num.Compute(contract.FormulaCode, in)
	if err != nil {
		p.BlockedReason = err.Error()
		p.Warnings = append(p.Warnings, "必要参数缺失或合同基准不匹配，暂停付款")
		return p, nil
	}
	p.Result = res
	p.CanPay = true
	return p, nil
}

type ComputeInput struct {
	Note string `json:"note"`
}

// ComputePayable creates a new current version. The previous current version is
// kept (superseded), never overwritten, and an adjustment row is written when
// the amount changes.
func (a *App) ComputePayable(ctx context.Context, actor User, tripID int64, note string) (Payable, error) {
	if err := RequireRole(actor, "finance", "purchasing"); err != nil {
		return Payable{}, err
	}
	var id int64
	err := a.DB.Tx(ctx, func(tx pgx.Tx) error {
		var err error
		id, err = a.computePayableTx(ctx, tx, actor, tripID, note)
		return err
	})
	if err != nil {
		return Payable{}, err
	}
	return a.GetPayable(ctx, id)
}

// computePayableTx writes a new current payable version inside an existing
// transaction. Adjudication reuses it to rebuild the payment basis for an
// unpaid trip after a determination.
func (a *App) computePayableTx(ctx context.Context, tx pgx.Tx, actor User, tripID int64, note string) (int64, error) {
	var id int64
	// Lock the trip to serialise concurrent computations and races on the same
	// remaining balance.
	var locked int64
	if err := tx.QueryRow(ctx, `select id from rt.trips where id=$1 for update`, tripID).Scan(&locked); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, notFound("trip %d", tripID)
		}
		return 0, err
	}
	preview, err := a.priceTrip(ctx, tx, tripID)
	if err != nil {
		return 0, err
	}
	if !preview.CanPay {
		return 0, blocked("无法计算应付：%s", preview.BlockedReason)
	}
	var prevID *int64
	var prevVersion int
	var prevAmount, prevMetal, prevWet int64
	err = tx.QueryRow(ctx, `select id, version, amount_cents, metal_mg, wet_net_g from rt.payables
		where trip_id=$1 and is_current for update`, tripID).Scan(&prevID, &prevVersion, &prevAmount, &prevMetal, &prevWet)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return 0, err
	}
	version := prevVersion + 1
	if prevID != nil {
		if _, err := tx.Exec(ctx, `update rt.payables set is_current=false, status='superseded' where id=$1`, *prevID); err != nil {
			return 0, err
		}
	}
	snapshot, err := json.Marshal(map[string]any{
		"formula": preview.FormulaCode, "steps": preview.Result,
		"contract": preview.ContractID, "adoption": preview.AdoptionID, "assay": preview.AssayID,
		"grade": preview.GradeID, "computed_at": time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		return 0, err
	}
	err = tx.QueryRow(ctx, `insert into rt.payables(trip_id, version, is_current, supersedes_id, weight_adoption_id,
		assay_id, grade_approval_id, contract_id, formula_snapshot, wet_net_g, dry_g, impurity_g, priced_g, metal_mg,
		coef_ppm, unit_price_cents_per_t, amount_cents, status, computed_by, note)
		values($1,$2,true,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,'calculated',$17,$18) returning id`,
		tripID, version, prevID, preview.AdoptionID, preview.AssayID, preview.GradeID, preview.ContractID,
		snapshot, preview.Result.WetNetG, preview.Result.DryG, preview.Result.ImpurityG, preview.Result.PricedG,
		preview.Result.MetalMg, preview.Result.CoefPPM, preview.Result.UnitPriceCPT, preview.Result.AmountCents,
		actor.ID, note).Scan(&id)
	if err != nil {
		return 0, err
	}
	if prevID != nil {
		if _, err := tx.Exec(ctx, `insert into rt.payment_ledger(payable_id, trip_id, delta_cents, old_wet_net_g,
			new_wet_net_g, old_metal_mg, new_metal_mg, reason, actor_id)
			values($1,$2,$3,$4,$5,$6,$7,$8,$9)`, id, tripID, preview.Result.AmountCents-prevAmount,
			prevWet, preview.Result.WetNetG, prevMetal, preview.Result.MetalMg, note, actor.ID); err != nil {
			return 0, err
		}
	}
	if err := insertAudit(ctx, tx, actor, "payable.compute", "payable", id, map[string]any{
		"trip_id": tripID, "version": version, "amount_cents": preview.Result.AmountCents,
		"metal_mg": preview.Result.MetalMg, "wet_net_g": preview.Result.WetNetG}); err != nil {
		return 0, err
	}
	return id, nil
}

// PreviewPayable returns the calculation and the difference that a new version
// would introduce, before finance approves it.
type PayablePreview struct {
	Calc             CalcPreview `json:"calculation"`
	Current          *Payable    `json:"current,omitempty"`
	DeltaWetNetG     int64       `json:"delta_wet_net_g"`
	DeltaMetalMg     int64       `json:"delta_metal_mg"`
	DeltaAmountCents int64       `json:"delta_amount_cents"`
}

func (a *App) PreviewPayable(ctx context.Context, tripID int64) (PayablePreview, error) {
	calc, err := a.priceTrip(ctx, a.DB.Pool, tripID)
	if err != nil {
		return PayablePreview{}, err
	}
	out := PayablePreview{Calc: calc}
	var curID int64
	err = a.DB.Pool.QueryRow(ctx, `select id from rt.payables where trip_id=$1 and is_current`, tripID).Scan(&curID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return out, nil
		}
		return out, err
	}
	cur, err := a.GetPayable(ctx, curID)
	if err != nil {
		return out, err
	}
	out.Current = &cur
	if calc.CanPay {
		out.DeltaWetNetG = calc.Result.WetNetG - cur.WetNetG
		out.DeltaMetalMg = calc.Result.MetalMg - cur.MetalMg
		out.DeltaAmountCents = calc.Result.AmountCents - cur.AmountCents
	}
	return out, nil
}

// ApprovePayable moves a calculated version to approved. Finance sees the delta
// first (PreviewPayable) and then approves.
func (a *App) ApprovePayable(ctx context.Context, actor User, payableID int64, note string) (Payable, error) {
	if err := RequireRole(actor, "finance"); err != nil {
		return Payable{}, err
	}
	err := a.DB.Tx(ctx, func(tx pgx.Tx) error {
		var tripID int64
		var status string
		var isCurrent bool
		err := tx.QueryRow(ctx, `select trip_id, status, is_current from rt.payables where id=$1 for update`, payableID).
			Scan(&tripID, &status, &isCurrent)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return notFound("payable %d", payableID)
			}
			return err
		}
		if !isCurrent {
			return conflict("只能批准当前有效版本")
		}
		if status != "calculated" && status != "held" {
			return conflict("应付款状态 %s 不能批准", status)
		}
		// A supplier dispute freezes only this truck's payment; other trucks of
		// the same supplier are unaffected.
		if frozen, reason, err := activeFreeze(ctx, tx, tripID); err != nil {
			return err
		} else if frozen {
			return blocked("本车处于复核争议冻结：%s", reason)
		}
		// Verify the approved grade still exists and is approved.
		var gradeStatus string
		if err := tx.QueryRow(ctx, `select status from rt.grade_approvals where trip_id=$1`, tripID).Scan(&gradeStatus); err != nil {
			return blocked("车次等级未批准，不能批准付款")
		}
		if gradeStatus != "approved" {
			return blocked("车次等级状态 %s，不能批准付款", gradeStatus)
		}
		_, err = tx.Exec(ctx, `update rt.payables set status='approved', approved_by=$2, approved_at=now(),
			note=case when $3='' then note else $3 end where id=$1`, payableID, actor.ID, note)
		if err != nil {
			return err
		}
		return insertAudit(ctx, tx, actor, "payable.approve", "payable", payableID, map[string]any{"trip_id": tripID})
	})
	if err != nil {
		return Payable{}, err
	}
	return a.GetPayable(ctx, payableID)
}

// MarkPaid confirms payment of an approved version.
func (a *App) MarkPaid(ctx context.Context, actor User, payableID int64) (Payable, error) {
	if err := RequireRole(actor, "finance"); err != nil {
		return Payable{}, err
	}
	err := a.DB.Tx(ctx, func(tx pgx.Tx) error {
		var tripID int64
		if err := tx.QueryRow(ctx, `select trip_id from rt.payables where id=$1`, payableID).Scan(&tripID); err != nil {
			return err
		}
		if frozen, reason, err := activeFreeze(ctx, tx, tripID); err != nil {
			return err
		} else if frozen {
			return blocked("本车处于复核争议冻结：%s", reason)
		}
		tag, err := tx.Exec(ctx, `update rt.payables set status='paid' where id=$1 and status='approved' and is_current`, payableID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return conflict("只有已批准的当前版本可以标记付款")
		}
		_, err = tx.Exec(ctx, `insert into rt.audit_log(actor_id, action, entity, entity_id) values($1,'payable.paid','payable',$2)`,
			actor.ID, fmt.Sprint(payableID))
		return err
	})
	if err != nil {
		return Payable{}, err
	}
	return a.GetPayable(ctx, payableID)
}

func (a *App) GetPayable(ctx context.Context, id int64) (Payable, error) {
	var p Payable
	var formulaCode string
	err := a.DB.Pool.QueryRow(ctx, `select p.id, p.trip_id, t.batch_no, p.version, p.is_current, p.supersedes_id,
		p.wet_net_g, p.dry_g, p.impurity_g, p.priced_g, p.metal_mg, p.coef_ppm, p.unit_price_cents_per_t, p.amount_cents,
		coalesce(c.formula->>'code',''), p.formula_snapshot, p.status, p.hold_reason, p.weight_adoption_id, p.assay_id,
		p.grade_approval_id, p.contract_id, to_char(p.computed_at,'YYYY-MM-DD"T"HH24:MI:SSOF'),
		to_char(p.approved_at,'YYYY-MM-DD"T"HH24:MI:SSOF'), p.note
		from rt.payables p join rt.trips t on t.id=p.trip_id left join rt.contracts c on c.id=p.contract_id
		where p.id=$1`, id).
		Scan(&p.ID, &p.TripID, &p.BatchNo, &p.Version, &p.IsCurrent, &p.SupersedesID, &p.WetNetG, &p.DryG,
			&p.ImpurityG, &p.PricedG, &p.MetalMg, &p.CoefPPM, &p.UnitPriceCPT, &p.AmountCents, &formulaCode,
			&p.FormulaSnapshot, &p.Status, &p.HoldReason, &p.WeightAdoptionID, &p.AssayID, &p.GradeApprovalID,
			&p.ContractID, &p.ComputedAt, &p.ApprovedAt, &p.Note)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return p, notFound("payable %d", id)
		}
		return p, err
	}
	p.FormulaCode = formulaCode
	if f, ok := num.Formulas[formulaCode]; ok {
		p.Formula = f
	}
	// Derive moisture from stored wet net & dry.
	p.MoistureG = p.WetNetG - p.DryG
	adj, err := a.payableAdjustments(ctx, id)
	if err != nil {
		return p, err
	}
	p.Adjustments = adj
	p.Frozen, p.FreezeReason, _ = activeFreeze(ctx, a.DB.Pool, p.TripID)
	return p, nil
}

func (a *App) payableAdjustments(ctx context.Context, payableID int64) ([]Adjustment, error) {
	rows, err := a.DB.Pool.Query(ctx, `select id, delta_cents, old_wet_net_g, new_wet_net_g, old_metal_mg, new_metal_mg,
		reason, to_char(created_at,'YYYY-MM-DD"T"HH24:MI:SSOF') from rt.payment_ledger
		where payable_id=$1 order by id`, payableID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Adjustment{}
	for rows.Next() {
		var a Adjustment
		if err := rows.Scan(&a.ID, &a.DeltaCents, &a.OldWetNetG, &a.NewWetNetG, &a.OldMetalMg, &a.NewMetalMg,
			&a.Reason, &a.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

type PayableListItem struct {
	Payable
	VersionCount int   `json:"version_count"`
	PaidCents    int64 `json:"paid_cents"`
}

func (a *App) ListPayables(ctx context.Context) ([]Payable, error) {
	rows, err := a.DB.Pool.Query(ctx, `select id from rt.payables order by trip_id, version desc`)
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
	out := []Payable{}
	for _, id := range ids {
		p, err := a.GetPayable(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

type PayableVersions struct {
	TripID         int64     `json:"trip_id"`
	BatchNo        string    `json:"batch_no"`
	Versions       []Payable `json:"versions"`
	TotalPaidCents int64     `json:"total_paid_cents"`
}

func (a *App) PayableVersionsForTrip(ctx context.Context, tripID int64) (PayableVersions, error) {
	var out PayableVersions
	if err := a.DB.Pool.QueryRow(ctx, `select batch_no from rt.trips where id=$1`, tripID).Scan(&out.BatchNo); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return out, notFound("trip %d", tripID)
		}
		return out, err
	}
	out.TripID = tripID
	rows, err := a.DB.Pool.Query(ctx, `select id from rt.payables where trip_id=$1 order by version`, tripID)
	if err != nil {
		return out, err
	}
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return out, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	out.Versions = []Payable{}
	for _, id := range ids {
		p, err := a.GetPayable(ctx, id)
		if err != nil {
			return out, err
		}
		out.Versions = append(out.Versions, p)
		if p.Status == "paid" {
			out.TotalPaidCents += p.AmountCents
		}
	}
	return out, nil
}

// computeTripMetal runs the contract formula for a trip and returns the priced
// metal. It requires adopted weights, an adopted assay and an approved grade.
func computeTripMetal(ctx context.Context, q querier, tripID int64) (num.Result, error) {
	adoption, err := currentAdoption(ctx, q, tripID)
	if err != nil {
		return num.Result{}, err
	}
	assay, err := effectiveAssay(ctx, q, tripID)
	if err != nil {
		return num.Result{}, err
	}
	var contractID int64
	if err := q.QueryRow(ctx, `select contract_id from rt.trips where id=$1`, tripID).Scan(&contractID); err != nil {
		return num.Result{}, err
	}
	c, err := getContract(ctx, q, contractID)
	if err != nil {
		return num.Result{}, err
	}
	var coef int64
	if err := q.QueryRow(ctx, `select coef_ppm from rt.grade_approvals where trip_id=$1 and status='approved'`, tripID).Scan(&coef); err != nil {
		return num.Result{}, blocked("车次 %d 等级未批准", tripID)
	}
	return num.Compute(c.FormulaCode, num.Inputs{
		WetNetG: adoption.NetG,
		Assay: num.AssayInput{MoisturePPM: assay.MoisturePPM, ImpurityPPM: assay.ImpurityPPM,
			ImpurityBasis: assay.ImpurityBasis, MetalPPM: assay.MetalPPM, MetalBasis: assay.MetalBasis},
		CoefPPM: coef, UnitPriceCPT: c.UnitPriceCentsPerT,
	})
}

func getContract(ctx context.Context, q querier, id int64) (Contract, error) {
	var c Contract
	var formulaCode string
	err := q.QueryRow(ctx, `select id, code, supplier_id, name, metal_basis, unit_price_cents_per_t, active, note,
		coalesce(formula->>'code','') from rt.contracts where id=$1`, id).
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
	return c, nil
}

// ReassayResult reports which stock and payables a re-issued assay touches.
type ReassayResult struct {
	TripID           int64           `json:"trip_id"`
	OldIntakeMetalMg int64           `json:"old_intake_metal_mg"`
	NewIntakeMetalMg int64           `json:"new_intake_metal_mg"`
	AffectedLots     []AffectedLot   `json:"affected_lots"`
	Payable          *PayablePreview `json:"payable,omitempty"`
}

type AffectedLot struct {
	LotID      int64  `json:"lot_id"`
	LotKey     string `json:"lot_key"`
	PileName   string `json:"pile_name"`
	PileKind   string `json:"pile_kind"`
	State      string `json:"state"`
	WeightG    int64  `json:"weight_g"`
	OldMetalMg int64  `json:"old_metal_mg"`
	NewMetalMg int64  `json:"new_metal_mg"`
}

// ApplyReassay propagates a quality-adopted re-test to every stock node holding
// the trip's material, following the origin ledger. Shipped and production lots
// are adjusted in place — they are not reverted to "unused". The original
// payable is kept; a new version is produced separately by ComputePayable.
func (a *App) ApplyReassay(ctx context.Context, actor User, tripID int64) (ReassayResult, error) {
	if err := RequireRole(actor, "quality", "finance"); err != nil {
		return ReassayResult{}, err
	}
	var out ReassayResult
	err := a.DB.Tx(ctx, func(tx pgx.Tx) error {
		var err error
		out, err = a.applyReassayTx(ctx, tx, actor, tripID)
		return err
	})
	if err != nil {
		return out, err
	}
	// Attach a payable preview outside the transaction.
	if pv, err := a.PreviewPayable(ctx, tripID); err == nil {
		out.Payable = &pv
	}
	return out, nil
}

// applyReassayTx propagates an adopted re-test to every stock node holding the
// trip's material inside an existing transaction. Adjudication reuses it so a
// determination changes both the payment basis and the mixed-pile metal in one
// commit.
func (a *App) applyReassayTx(ctx context.Context, tx pgx.Tx, actor User, tripID int64) (ReassayResult, error) {
	var out ReassayResult
	out.TripID = tripID
	{
		var locked int64
		if err := tx.QueryRow(ctx, `select id from rt.trips where id=$1 for update`, tripID).Scan(&locked); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return out, notFound("trip %d", tripID)
			}
			return out, err
		}
		// The trip's intake net weight is the adopted weighing; the material may
		// now live in downstream lots, so never read it from the original lot.
		var intakeW int64
		err := tx.QueryRow(ctx, `select net_g from rt.weight_adoptions where trip_id=$1 and is_current`, tripID).
			Scan(&intakeW)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return out, blocked("车次 %d 尚无采用称次", tripID)
			}
			return out, err
		}
		var origMetal int64
		if err := tx.QueryRow(ctx, `select coalesce(sum(metal_mg),0) from rt.lot_origins where trip_id=$1`, tripID).
			Scan(&origMetal); err != nil {
			return out, err
		}
		oldMetal := origMetal
		pricing, err := computeTripMetal(ctx, tx, tripID)
		if err != nil {
			return out, err
		}
		newTotal := pricing.MetalMg
		out.OldIntakeMetalMg = oldMetal
		out.NewIntakeMetalMg = newTotal
		if intakeW <= 0 {
			return out, blocked("车次入库净重为 0，无法按比例调整")
		}

		// Find every LIVE lot still holding this trip's origin. Consumed source
		// lots have had their origins reduced to zero and are skipped. Production
		// and shipped lots are adjusted in place rather than reverted.
		type lotOrig struct {
			lotID  int64
			weight int64
			old    int64
		}
		rows, err := tx.Query(ctx, `select o.lot_id, o.weight_g, o.metal_mg from rt.lot_origins o
			join rt.inventory_lots l on l.id=o.lot_id
			where o.trip_id=$1 and o.weight_g > 0 and l.weight_g > 0 order by o.lot_id`, tripID)
		if err != nil {
			return out, err
		}
		var list []lotOrig
		for rows.Next() {
			var lo lotOrig
			if err := rows.Scan(&lo.lotID, &lo.weight, &lo.old); err != nil {
				rows.Close()
				return out, err
			}
			list = append(list, lo)
		}
		rows.Close()
		if len(list) == 0 {
			return out, blocked("车次 %d 没有存量来源记录", tripID)
		}
		// New metal per origin weight; the sum must equal newTotal exactly.
		// Scale metal in proportion to each live origin's CURRENT metal, not its
		// weight. A drying-loss node carries no metal and must stay at zero; a
		// purely weight-proportional split would wrongly load it with metal and
		// then starve the material that actually holds the copper.
		oldMetals := make([]int64, len(list))
		var oldLiveMetal int64
		for i, lo := range list {
			oldMetals[i] = lo.old
			oldLiveMetal += lo.old
		}
		if oldLiveMetal <= 0 {
			return out, blocked("车次 %d 存量不含金属，无法按品位调整", tripID)
		}
		newVals := num.Alloc(newTotal, oldMetals)
		_ = intakeW
		for i, lo := range list {
			if lo.weight <= 0 {
				continue
			}
			delta := newVals[i] - lo.old
			if delta == 0 {
				continue
			}
			if _, err := tx.Exec(ctx, `update rt.lot_origins set metal_mg=$3 where lot_id=$1 and trip_id=$2`,
				lo.lotID, tripID, newVals[i]); err != nil {
				return out, err
			}
			if _, err := tx.Exec(ctx, `update rt.inventory_lots set metal_mg=greatest(metal_mg+$2,0), updated_at=now()
				where id=$1`, lo.lotID, delta); err != nil {
				return out, err
			}
			if _, err := tx.Exec(ctx, `insert into rt.lot_events(lot_id, event, weight_g, metal_mg, pile_id, occurred_at, detail)
				select $1,'metal_adjust',$2,$3,pile_id, now(), jsonb_build_object('trip_id',$4::bigint,'delta_mg',$5::bigint)
				from rt.inventory_lots where id=$1`, lo.lotID, lo.weight, newVals[i], tripID, delta); err != nil {
				return out, err
			}
			var key, pileName, pileKind, state string
			var w int64
			if err := tx.QueryRow(ctx, `select l.lot_key, p.name, p.kind, l.state, l.weight_g
				from rt.inventory_lots l join rt.piles p on p.id=l.pile_id where l.id=$1`, lo.lotID).
				Scan(&key, &pileName, &pileKind, &state, &w); err != nil {
				return out, err
			}
			out.AffectedLots = append(out.AffectedLots, AffectedLot{LotID: lo.lotID, LotKey: key,
				PileName: pileName, PileKind: pileKind, State: state, WeightG: w,
				OldMetalMg: lo.old, NewMetalMg: newVals[i]})
		}
		if out.AffectedLots == nil {
			out.AffectedLots = []AffectedLot{}
		}
		if err := insertAudit(ctx, tx, actor, "assay.reassay", "trip", tripID, map[string]any{
			"old_metal_mg": oldMetal, "new_metal_mg": newTotal, "lots": len(out.AffectedLots)}); err != nil {
			return out, err
		}
	}
	return out, nil
}
