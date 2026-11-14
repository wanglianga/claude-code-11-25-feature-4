package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// ---------------------------------------------------------------------------
// 留样库存：原封样登记、分样、封签流转、剩余量。外送检测一次占用并扣除。
// ---------------------------------------------------------------------------

const (
	UnitSealed = "sealed"
	UnitDrawn  = "drawn"
	UnitBroken = "broken"
	UnitVoid   = "void"
)

type SampleUnit struct {
	ID           int64       `json:"id"`
	UnitNo       string      `json:"unit_no"`
	SampleID     int64       `json:"sample_id"`
	SampleNo     string      `json:"sample_no"`
	TripID       int64       `json:"trip_id"`
	BatchNo      string      `json:"batch_no"`
	ParentUnitID *int64      `json:"parent_unit_id"`
	Origin       string      `json:"origin"`
	Position     string      `json:"position"`
	SealNo       string      `json:"seal_no"`
	InitialG     int64       `json:"initial_g"`
	RemainingG   int64       `json:"remaining_g"`
	Status       string      `json:"status"`
	Note         string      `json:"note"`
	Drawn        bool        `json:"drawn"`
	DrawID       *int64      `json:"draw_id"`
	BlindCode    string      `json:"blind_code"`
	CreatedAt    time.Time   `json:"created_at"`
	SealEvents   []SealEvent `json:"seal_events"`
}

type SealEvent struct {
	ID           int64     `json:"id"`
	UnitID       int64     `json:"unit_id"`
	Event        string    `json:"event"`
	SealNo       string    `json:"seal_no"`
	PreviousSeal string    `json:"previous_seal"`
	WeightG      int64     `json:"weight_g"`
	Detail       any       `json:"detail"`
	OccurredAt   time.Time `json:"occurred_at"`
}

type RegisterUnitInput struct {
	UnitNo   string `json:"unit_no"`
	Position string `json:"position"`
	SealNo   string `json:"seal_no"`
	InitialG int64  `json:"initial_g"`
	Note     string `json:"note"`
}

// RegisterSealedUnit records the original retained sample for an existing
// sealed sample. The sample already binds trip + position; this adds physical
// quantity and the seal that will be circulated.
func (a *App) RegisterSealedUnit(ctx context.Context, actor User, sampleID int64, in RegisterUnitInput) (SampleUnit, error) {
	if err := RequireRole(actor, "lab", "yard"); err != nil {
		return SampleUnit{}, err
	}
	if in.InitialG <= 0 {
		return SampleUnit{}, invalid("留样初始样量 initial_g 必须为正（克）")
	}
	if in.SealNo == "" {
		return SampleUnit{}, invalid("封签号 seal_no 必填：封签是后续复核的物理依据")
	}
	var unitID int64
	err := a.DB.Tx(ctx, func(tx pgx.Tx) error {
		var sampleNo, tripPos string
		var tripID int64
		err := tx.QueryRow(ctx, `select s.sample_no, s.position, s.trip_id from rt.samples s where s.id=$1`, sampleID).
			Scan(&sampleNo, &tripPos, &tripID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return notFound("sample %d", sampleID)
			}
			return err
		}
		if in.Position == "" {
			in.Position = tripPos
		}
		unitNo := in.UnitNo
		if unitNo == "" {
			unitNo = fmt.Sprintf("%s-U1", sampleNo)
		}
		var exists bool
		if err := tx.QueryRow(ctx, `select exists(select 1 from rt.sample_units where sample_id=$1 and origin='original')`,
			sampleID).Scan(&exists); err != nil {
			return err
		}
		if exists {
			return conflict("封样 %s 已登记原留样，拆分请使用分样接口", sampleNo)
		}
		err = tx.QueryRow(ctx, `insert into rt.sample_units(unit_no, sample_id, origin, position, seal_no, initial_g,
			remaining_g, status, note, created_by) values($1,$2,'original',$3,$4,$5,$5,'sealed',$6,$7) returning id`,
			unitNo, sampleID, in.Position, in.SealNo, in.InitialG, in.Note, actor.ID).Scan(&unitID)
		if err != nil {
			return conflict("unit_no 或封签重复：%s", unitNo)
		}
		if _, err := tx.Exec(ctx, `insert into rt.sample_seal_events(unit_id, event, seal_no, weight_g, detail, actor_id, occurred_at)
			values($1,'sealed',$2,$3,$4,$5,$6)`, unitID, in.SealNo, in.InitialG,
			map[string]any{"trip_id": tripID, "sample_id": sampleID}, actor.ID, time.Now()); err != nil {
			return err
		}
		return insertAudit(ctx, tx, actor, "sample_unit.register", "sample_unit", unitID, map[string]any{
			"sample_id": sampleID, "seal_no": in.SealNo, "initial_g": in.InitialG})
	})
	if err != nil {
		return SampleUnit{}, err
	}
	return a.GetSampleUnit(ctx, unitID)
}

type SplitUnitInput struct {
	UnitNo  string `json:"unit_no"`
	SealNo  string `json:"seal_no"`
	WeightG int64  `json:"weight_g"`
	Note    string `json:"note"`
}

// SplitSampleUnit takes a declared amount out of a parent unit into a new
// child unit with its own seal. Parent and child relationship is recorded, so
// a review of the child traces back to the original sealed sample. A unit that
// has already been drawn to an external lab cannot be split further.
func (a *App) SplitSampleUnit(ctx context.Context, actor User, parentID int64, in SplitUnitInput) (SampleUnit, error) {
	if err := RequireRole(actor, "lab", "yard"); err != nil {
		return SampleUnit{}, err
	}
	if in.WeightG <= 0 {
		return SampleUnit{}, invalid("分样重量 weight_g 必须为正")
	}
	if in.SealNo == "" {
		return SampleUnit{}, invalid("分样需要独立封签号 seal_no，否则无法判断流转关系")
	}
	var outID int64
	err := a.DB.Tx(ctx, func(tx pgx.Tx) error {
		var unitNoDB, position, status, parentSeal, sampleNo string
		var remain, initial, sampleID int64
		err := tx.QueryRow(ctx, `select u.unit_no, u.position, u.seal_no, u.status, u.remaining_g, u.initial_g,
			s.sample_no, u.sample_id from rt.sample_units u join rt.samples s on s.id=u.sample_id where u.id=$1 for update of u`,
			parentID).Scan(&unitNoDB, &position, &parentSeal, &status, &remain, &initial, &sampleNo, &sampleID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return notFound("sample unit %d", parentID)
			}
			return err
		}
		switch status {
		case UnitBroken:
			return blocked("留样 %d 封签已断裂，不能分样；须先补证", parentID)
		case UnitDrawn:
			return blocked("留样 %d 已外送领取，不能再分样", parentID)
		case UnitVoid:
			return blocked("留样 %d 已作废", parentID)
		}
		if remain < in.WeightG {
			return blocked("留样 %d 剩余 %d g，不足分样 %d g", parentID, remain, in.WeightG)
		}
		newRemain := remain - in.WeightG
		if _, err := tx.Exec(ctx, `update rt.sample_units set remaining_g=$2 where id=$1`, parentID, newRemain); err != nil {
			return err
		}
		// Derive a unit number from the parent if not supplied.
		unitNo := in.UnitNo
		if unitNo == "" {
			var n int
			if err := tx.QueryRow(ctx, `select count(*)+1 from rt.sample_units where parent_unit_id=$1`, parentID).Scan(&n); err != nil {
				return err
			}
			unitNo = fmt.Sprintf("%s-S%d", sampleNo, n)
		}
		err = tx.QueryRow(ctx, `insert into rt.sample_units(unit_no, sample_id, parent_unit_id, origin, position, seal_no,
			initial_g, remaining_g, status, note, created_by)
			values($1,$2,$3,'split',$4,$5,$6,$6,'sealed',$7,$8) returning id`,
			unitNo, sampleID, parentID, position, in.SealNo, in.WeightG, in.Note, actor.ID).Scan(&outID)
		if err != nil {
			return conflict("分样 unit_no 重复：%s", unitNo)
		}
		if _, err := tx.Exec(ctx, `insert into rt.sample_seal_events(unit_id, event, seal_no, previous_seal, weight_g, detail, actor_id, occurred_at)
			values($1,'split',$2,$3,$4,$5,$6,$7)`, outID, in.SealNo, parentSeal, in.WeightG,
			map[string]any{"parent_unit_id": parentID, "parent_remaining_g": newRemain}, actor.ID, time.Now()); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `insert into rt.sample_seal_events(unit_id, event, seal_no, weight_g, detail, actor_id, occurred_at)
			values($1,'reseal',$2,$3,$4,$5,$6)`, parentID, parentSeal, newRemain,
			map[string]any{"taken_by_split": outID, "split_weight_g": in.WeightG}, actor.ID, time.Now()); err != nil {
			return err
		}
		return insertAudit(ctx, tx, actor, "sample_unit.split", "sample_unit", outID, map[string]any{
			"parent_unit_id": parentID, "weight_g": in.WeightG})
	})
	if err != nil {
		return SampleUnit{}, err
	}
	return a.GetSampleUnit(ctx, outID)
}

type BreakSealInput struct {
	Reason string `json:"reason"`
	SealNo string `json:"seal_no"`
}

// BreakSeal marks a retained sample's seal as broken. A broken unit cannot be
// scheduled for re-testing; the reason is retained on the circulation log.
func (a *App) BreakSeal(ctx context.Context, actor User, unitID int64, in BreakSealInput) (SampleUnit, error) {
	if err := RequireRole(actor, "lab", "yard", "quality"); err != nil {
		return SampleUnit{}, err
	}
	if in.Reason == "" {
		return SampleUnit{}, invalid("封签断裂必须写明原因 reason")
	}
	err := a.DB.Tx(ctx, func(tx pgx.Tx) error {
		var seal, status string
		var remain int64
		err := tx.QueryRow(ctx, `select seal_no, status, remaining_g from rt.sample_units where id=$1 for update`,
			unitID).Scan(&seal, &status, &remain)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return notFound("sample unit %d", unitID)
			}
			return err
		}
		if status == UnitBroken {
			return conflict("留样 %d 已记录断裂", unitID)
		}
		if status == UnitDrawn {
			return conflict("留样 %d 已外送领取，不能改登记为断裂", unitID)
		}
		brokenSeal := in.SealNo
		if brokenSeal == "" {
			brokenSeal = seal
		}
		if _, err := tx.Exec(ctx, `update rt.sample_units set status='broken', remaining_g=0 where id=$1`, unitID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `insert into rt.sample_seal_events(unit_id, event, seal_no, previous_seal, weight_g, detail, actor_id, occurred_at)
			values($1,'broken',$2,$3,$4,$5,$6,$7)`, unitID, brokenSeal, seal, remain,
			map[string]any{"reason": in.Reason}, actor.ID, time.Now()); err != nil {
			return err
		}
		return insertAudit(ctx, tx, actor, "sample_unit.break", "sample_unit", unitID, map[string]any{"reason": in.Reason})
	})
	if err != nil {
		return SampleUnit{}, err
	}
	return a.GetSampleUnit(ctx, unitID)
}

type DrawInput struct {
	WeightG   int64  `json:"weight_g"`
	BlindCode string `json:"blind_code"`
	LabName   string `json:"lab_name"`
}

// DrawSampleUnit reserves and deducts the requested quantity for an external
// lab run. The unique unit_id on sample_draws is the hard guarantee that the
// same retained sample can never be used twice. A broken seal or insufficient
// quantity blocks the draw instead of scheduling it anyway.
func (a *App) DrawSampleUnit(ctx context.Context, actor User, caseID, unitID int64, in DrawInput) (DrawResult, error) {
	if err := RequireRole(actor, "lab", "yard", "quality"); err != nil {
		return DrawResult{}, err
	}
	if in.WeightG <= 0 {
		return DrawResult{}, invalid("领取样量 weight_g 必须为正")
	}
	var out DrawResult
	err := a.DB.Tx(ctx, func(tx pgx.Tx) error {
		var status string
		var remain int64
		var sampleID, tripID int64
		var caseUnitID *int64
		var caseStatus string
		var evVersion int
		err := tx.QueryRow(ctx, `select u.status, u.remaining_g, u.sample_id, s.trip_id, c.sample_unit_id, c.status, c.evidence_version
			from rt.sample_units u join rt.samples s on s.id=u.sample_id
			join rt.review_cases c on c.id=$2
			where u.id=$1 for update of u, c`, unitID, caseID).
			Scan(&status, &remain, &sampleID, &tripID, &caseUnitID, &caseStatus, &evVersion)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return notFound("sample unit %d or case %d", unitID, caseID)
			}
			return err
		}
		var caseSampleID int64
		if err := tx.QueryRow(ctx, `select sample_id from rt.review_cases where id=$1`, caseID).Scan(&caseSampleID); err != nil {
			return err
		}
		if caseSampleID != sampleID {
			return blocked("留样 %d 属于封样 %d，与本案封样 %d 不同：复核必须围绕同一车材料", unitID, sampleID, caseSampleID)
		}
		switch status {
		case UnitBroken:
			return blocked("留样封签已断裂，不能照常安排复检；请补充封签证据后重新开案")
		case UnitDrawn:
			return blocked("该留样已外送领取，同一留样不能重复使用；请使用分样单元")
		case UnitVoid:
			return blocked("留样已作废")
		}
		if remain < in.WeightG {
			return blocked("留样剩余 %d g，不足领取 %d g；不能安排复检", remain, in.WeightG)
		}
		if caseStatus == "adjudicated" || caseStatus == "settled" {
			return conflict("案件已裁定，新增检测只能作为重新复核申请，不能直接领取同一案")
		}
		blind := in.BlindCode
		if blind == "" {
			blind = fmt.Sprintf("BL-%d-%d", caseID, unitID)
		}
		// Attach the case to this physical unit if it has none (first draw).
		if caseUnitID == nil {
			if _, err := tx.Exec(ctx, `update rt.review_cases set sample_unit_id=$2 where id=$1`, caseID, unitID); err != nil {
				return err
			}
		} else if *caseUnitID != unitID {
			// The case already points at a different unit; ensure they share the
			// same sample so the material is genuinely the same truck.
			var otherSample int64
			if err := tx.QueryRow(ctx, `select sample_id from rt.sample_units where id=$1`, *caseUnitID).Scan(&otherSample); err != nil {
				return err
			}
			if otherSample != sampleID {
				return blocked("领取留样与原案留样不属于同一封样，需先补证关系")
			}
		}
		newRemain := remain - in.WeightG
		status2 := UnitDrawn
		if newRemain > 0 {
			// Partially drawn: keep it sealed for a possible different case, but
			// mark drawn so this exact unit cannot be drawn again.
			status2 = UnitDrawn
		}
		if _, err := tx.Exec(ctx, `update rt.sample_units set remaining_g=$2, status=$3 where id=$1`,
			unitID, newRemain, status2); err != nil {
			return err
		}
		var drawID int64
		err = tx.QueryRow(ctx, `insert into rt.sample_draws(unit_id, case_id, purpose, weight_g, blind_code, lab_name, drawn_by)
			values($1,$2,'external_check',$3,$4,$5,$6) returning id`,
			unitID, caseID, in.WeightG, blind, in.LabName, actor.ID).Scan(&drawID)
		if err != nil {
			return conflict("该留样已被领取或盲编号重复：%v", err)
		}
		if _, err := tx.Exec(ctx, `insert into rt.sample_seal_events(unit_id, event, seal_no, weight_g, detail, actor_id, occurred_at)
			select $1,'draw', seal_no, $2, $3, $4, $5 from rt.sample_units where id=$1`,
			unitID, in.WeightG, map[string]any{"case_id": caseID, "blind_code": blind, "remaining_g": newRemain},
			actor.ID, time.Now()); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `insert into rt.external_reports(case_id, blind_code, lab_name, sample_unit_id, draw_id, status)
			values($1,$2,$3,$4,$5,'awaiting')`, caseID, blind, in.LabName, unitID, drawID); err != nil {
			return err
		}
		out = DrawResult{CaseID: caseID, UnitID: unitID, DrawID: drawID, BlindCode: blind,
			WeightG: in.WeightG, RemainingG: newRemain, EvidenceVersion: evVersion}
		return insertAudit(ctx, tx, actor, "sample.draw", "sample_unit", unitID, map[string]any{
			"case_id": caseID, "blind_code": blind, "weight_g": in.WeightG})
	})
	return out, err
}

type DrawResult struct {
	CaseID          int64  `json:"case_id"`
	UnitID          int64  `json:"unit_id"`
	DrawID          int64  `json:"draw_id"`
	BlindCode       string `json:"blind_code"`
	WeightG         int64  `json:"weight_g"`
	RemainingG      int64  `json:"remaining_g"`
	EvidenceVersion int    `json:"evidence_version"`
}

func (a *App) GetSampleUnit(ctx context.Context, id int64) (SampleUnit, error) {
	var u SampleUnit
	var drawID *int64
	var blind string
	err := a.DB.Pool.QueryRow(ctx, `select u.id, u.unit_no, u.sample_id, s.sample_no, s.trip_id, t.batch_no,
		u.parent_unit_id, u.origin, u.position, u.seal_no, u.initial_g, u.remaining_g, u.status, u.note, u.created_at,
		d.id, coalesce(d.blind_code,'')
		from rt.sample_units u join rt.samples s on s.id=u.sample_id join rt.trips t on t.id=s.trip_id
		left join rt.sample_draws d on d.unit_id=u.id
		where u.id=$1`, id).
		Scan(&u.ID, &u.UnitNo, &u.SampleID, &u.SampleNo, &u.TripID, &u.BatchNo, &u.ParentUnitID, &u.Origin,
			&u.Position, &u.SealNo, &u.InitialG, &u.RemainingG, &u.Status, &u.Note, &u.CreatedAt, &drawID, &blind)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return u, notFound("sample unit %d", id)
		}
		return u, err
	}
	u.DrawID = drawID
	u.Drawn = drawID != nil
	u.BlindCode = blind
	u.SealEvents = []SealEvent{}
	rows, err := a.DB.Pool.Query(ctx, `select id, unit_id, event, seal_no, previous_seal, weight_g, detail, occurred_at
		from rt.sample_seal_events where unit_id=$1 order by id`, id)
	if err != nil {
		return u, err
	}
	defer rows.Close()
	for rows.Next() {
		var ev SealEvent
		if err := rows.Scan(&ev.ID, &ev.UnitID, &ev.Event, &ev.SealNo, &ev.PreviousSeal, &ev.WeightG, &ev.Detail,
			&ev.OccurredAt); err != nil {
			return u, err
		}
		u.SealEvents = append(u.SealEvents, ev)
	}
	return u, rows.Err()
}

// ListSampleUnits returns retained units for a trip, including draws and seal
// history, so every conclusion can be traced to a physical sample.
func (a *App) ListSampleUnits(ctx context.Context, tripID int64) ([]SampleUnit, error) {
	rows, err := a.DB.Pool.Query(ctx, `select u.id from rt.sample_units u join rt.samples s on s.id=u.sample_id
		where s.trip_id=$1 order by u.id`, tripID)
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
	out := []SampleUnit{}
	for _, id := range ids {
		u, err := a.GetSampleUnit(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, nil
}
