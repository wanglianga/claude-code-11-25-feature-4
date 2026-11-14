package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"recycletrace/backend/internal/num"
)

// ---------------------------------------------------------------------------
// 供应商与工厂之间的封样复核与差异裁定
//
// 流程：开案（冻结本车付款）→ 登记双方报告 → 核对单位/基准/取样代表性 →
// 可比则按约定误差比对；不可比只说明差异来源，不自动择高/择低/平均 →
// 必要时外送盲样，凭盲编号返回并正式揭示 → 试算受影响车款与混堆去向 →
// 裁定采用范围与理由并结算（已付追补、未付重建）→ 裁定后新报告转重新复核。
// ---------------------------------------------------------------------------

const (
	CaseOpen            = "open"
	CaseComparing       = "comparing"
	CasePendingEvidence = "pending_evidence"
	CaseAdjudicated     = "adjudicated"
	CaseSettled         = "settled"
)

// Recognized ratio units. Values are normalized to ppm before storage; the
// declared unit is kept so a mismatch can be explained rather than silently
// averaged.
var ratioUnitFactor = map[string]int64{
	"%":     10_000,
	"ppm":   1,
	"mg/kg": 1,
	"g/kg":  1_000,
	"kg/t":  1_000,
	"g/t":   1,
}

func normalizeRatioUnit(u string) (string, bool) {
	if u == "" {
		return "ppm", true
	}
	key := strings.ToLower(strings.TrimSpace(u))
	key = strings.ReplaceAll(key, "％", "%")
	_, ok := ratioUnitFactor[key]
	if !ok {
		return key, false
	}
	return key, true
}

// DisplayPPM converts a normalized ppm ratio back to the declared unit for
// explanation text.
func ppmToUnit(ppm int64, unit string) string {
	f, ok := ratioUnitFactor[unit]
	if !ok || f == 0 {
		f = 1
	}
	v := float64(ppm) / float64(f)
	return fmt.Sprintf("%s %s", strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.6f", v), "0"), "."), unit)
}

type PaymentFreeze struct {
	ID         int64      `json:"id"`
	CaseID     int64      `json:"case_id"`
	TripID     int64      `json:"trip_id"`
	Reason     string     `json:"reason"`
	Status     string     `json:"status"`
	FrozenAt   time.Time  `json:"frozen_at"`
	ReleasedAt *time.Time `json:"released_at"`
}

type RereviewRequest struct {
	ID             int64     `json:"id"`
	CaseID         int64     `json:"case_id"`
	AdjudicationID *int64    `json:"adjudication_id"`
	Reason         string    `json:"reason"`
	Status         string    `json:"status"`
	RequestedAt    time.Time `json:"requested_at"`
}

type PartyReport struct {
	ID             int64     `json:"id"`
	CaseID         int64     `json:"case_id"`
	Party          string    `json:"party"`
	ReportNo       string    `json:"report_no"`
	LabName        string    `json:"lab_name"`
	SampleNo       string    `json:"sample_no"`
	SealNo         string    `json:"seal_no"`
	SplitFrom      string    `json:"split_from"`
	Position       string    `json:"position"`
	DeclaredUnit   string    `json:"declared_unit"`
	UnitOK         bool      `json:"unit_ok"`
	MoisturePPM    int64     `json:"moisture_ppm"`
	ImpurityPPM    *int64    `json:"impurity_ppm"`
	ImpurityBasis  string    `json:"impurity_basis"`
	MetalPPM       int64     `json:"metal_ppm"`
	MetalBasis     string    `json:"metal_basis"`
	QuoteCentsPerT int64     `json:"quote_cents_per_t,omitempty"`
	RelationOK     bool      `json:"relation_ok"`
	ReceivedAt     time.Time `json:"received_at"`
}

type ExternalReport struct {
	ID            int64      `json:"id"`
	CaseID        int64      `json:"case_id"`
	BlindCode     string     `json:"blind_code"`
	LabName       string     `json:"lab_name"`
	SampleUnitID  *int64     `json:"sample_unit_id"`
	Status        string     `json:"status"`
	MatchStatus   string     `json:"match_status"`
	DeclaredUnit  string     `json:"declared_unit"`
	UnitOK        bool       `json:"unit_ok"`
	MoisturePPM   int64      `json:"moisture_ppm"`
	ImpurityPPM   *int64     `json:"impurity_ppm"`
	ImpurityBasis string     `json:"impurity_basis"`
	MetalPPM      int64      `json:"metal_ppm"`
	MetalBasis    string     `json:"metal_basis"`
	Position      string     `json:"position"`
	SampleNo      string     `json:"sample_no"`
	SealNo        string     `json:"seal_no"`
	Revealed      bool       `json:"revealed"`
	Masked        bool       `json:"masked"`
	Note          string     `json:"note"`
	ReceivedAt    *time.Time `json:"received_at"`
}

type Adjudication struct {
	ID                int64              `json:"id"`
	CaseID            int64              `json:"case_id"`
	DecisionSeq       int                `json:"decision_seq"`
	Adopted           string             `json:"adopted"`
	PartyReportID     *int64             `json:"party_report_id"`
	ExternalReportID  *int64             `json:"external_report_id"`
	ScopeTripIDs      []int64            `json:"scope_trip_ids"`
	Comparable        bool               `json:"comparable"`
	WithinTolerance   bool               `json:"within_tolerance"`
	DifferenceSources []string           `json:"difference_sources"`
	Reason            string             `json:"reason"`
	EvidenceVersion   int                `json:"evidence_version"`
	Status            string             `json:"status"`
	AdoptedAssayIDs   []int64            `json:"adopted_assay_ids"`
	DecidedBy         int64              `json:"decided_by"`
	DecidedAt         time.Time          `json:"decided_at"`
	Lines             []AdjudicationLine `json:"lines"`
}

type AdjudicationLine struct {
	TripID         int64  `json:"trip_id"`
	BatchNo        string `json:"batch_no"`
	OldAmountCents int64  `json:"old_amount_cents"`
	NewAmountCents int64  `json:"new_amount_cents"`
	DeltaCents     int64  `json:"delta_cents"`
	Settlement     string `json:"settlement"`
	PayableID      *int64 `json:"payable_id"`
	Note           string `json:"note"`
}

type ReviewCase struct {
	ID                   int64            `json:"id"`
	CaseNo               string           `json:"case_no"`
	TripID               int64            `json:"trip_id"`
	BatchNo              string           `json:"batch_no"`
	SupplierID           int64            `json:"supplier_id"`
	SupplierName         string           `json:"supplier_name"`
	SampleID             int64            `json:"sample_id"`
	SampleNo             string           `json:"sample_no"`
	SampleUnitID         *int64           `json:"sample_unit_id"`
	Status               string           `json:"status"`
	SamePosition         bool             `json:"same_position"`
	MetalTolerancePPM    int64            `json:"metal_tolerance_ppm"`
	MoistureTolerancePPM int64            `json:"moisture_tolerance_ppm"`
	EvidenceVersion      int              `json:"evidence_version"`
	OpenedAt             time.Time        `json:"opened_at"`
	Note                 string           `json:"note"`
	PartyReports         []PartyReport    `json:"party_reports"`
	ExternalReports      []ExternalReport `json:"external_reports"`
	Adjudications        []Adjudication   `json:"adjudications"`
	Freeze               *PaymentFreeze   `json:"freeze,omitempty"`
	OpenRereview         *RereviewRequest `json:"open_rereview,omitempty"`
	Comparability        *Comparability   `json:"comparability,omitempty"`
}

// Comparability is the evidence check a human must see before comparing. It
// never invents a middle value: if conditions are not comparable the only
// output is the list of difference sources.
type Comparability struct {
	Comparable           bool     `json:"comparable"`
	UnitOK               bool     `json:"unit_ok"`
	BasisOK              bool     `json:"basis_ok"`
	SamplingOK           bool     `json:"sampling_ok"`
	RelationOK           bool     `json:"relation_ok"`
	WithinTolerance      bool     `json:"within_tolerance"`
	MetalDeltaPPM        int64    `json:"metal_delta_ppm"`
	MoistureDeltaPPM     int64    `json:"moisture_delta_ppm"`
	MetalTolerancePPM    int64    `json:"metal_tolerance_ppm"`
	MoistureTolerancePPM int64    `json:"moisture_tolerance_ppm"`
	DifferenceSources    []string `json:"difference_sources"`
	AgreedRange          string   `json:"agreed_range"`
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// evaluateComparability compares two party reports on the same sealed sample.
// Units and bases must line up; sampling must be the same position (or be
// explicitly declared the same by quality); the report's seal/sample must
// trace to the retained unit. Tolerance is the agreed range, never a license
// to average.
func evaluateComparability(c *ReviewCase, s, p *PartyReport, unit *SampleUnit) Comparability {
	out := Comparability{
		MetalTolerancePPM: c.MetalTolerancePPM, MoistureTolerancePPM: c.MoistureTolerancePPM,
		DifferenceSources: []string{},
	}
	out.AgreedRange = fmt.Sprintf("金属 ±%s，水分 ±%s", ppmToUnit(c.MetalTolerancePPM, "ppm"), ppmToUnit(c.MoistureTolerancePPM, "ppm"))
	if s == nil || p == nil {
		out.DifferenceSources = append(out.DifferenceSources, "缺少一方报告，无法比对")
		return out
	}
	out.UnitOK = s.UnitOK && p.UnitOK
	if !out.UnitOK {
		out.DifferenceSources = append(out.DifferenceSources,
			fmt.Sprintf("单位未登记或不可换算（供应商=%q，厂内=%q）：先补登记单位，不能按数值直接比较", s.DeclaredUnit, p.DeclaredUnit))
	} else if s.DeclaredUnit != p.DeclaredUnit {
		out.DifferenceSources = append(out.DifferenceSources,
			fmt.Sprintf("双方申报单位不同（供应商=%s，厂内=%s）：数值已统一按 ppm 存储后再比较，不按不同单位的字面值直接比大小",
				s.DeclaredUnit, p.DeclaredUnit))
	}
	out.BasisOK = s.MetalBasis != "" && s.MetalBasis == p.MetalBasis
	if s.ImpurityBasis != "" && p.ImpurityBasis != "" && s.ImpurityBasis != p.ImpurityBasis {
		out.BasisOK = false
	}
	if !out.BasisOK {
		out.DifferenceSources = append(out.DifferenceSources,
			fmt.Sprintf("干湿基不可比（供应商金属=%s/杂物=%s，厂内金属=%s/杂物=%s）：同一比例必须换算到同一基准后才能比对，不能把两份品位直接平均",
				s.MetalBasis, s.ImpurityBasis, p.MetalBasis, p.ImpurityBasis))
	}
	out.SamplingOK = strings.TrimSpace(s.Position) == strings.TrimSpace(p.Position) || c.SamePosition
	if !out.SamplingOK {
		out.DifferenceSources = append(out.DifferenceSources,
			fmt.Sprintf("取样位置不同（供应商=%s，厂内=%s），取样代表性不一致", s.Position, p.Position))
	}
	out.RelationOK = true
	if unit != nil {
		if p.SealNo != "" && p.SealNo != unit.SealNo {
			out.RelationOK = false
			out.DifferenceSources = append(out.DifferenceSources,
				fmt.Sprintf("厂内报告封签 %s 与留样封签 %s 不一致，取样关系不明", p.SealNo, unit.SealNo))
		}
		if p.SampleNo != "" && p.SampleNo != unit.SampleNo {
			out.RelationOK = false
			out.DifferenceSources = append(out.DifferenceSources,
				fmt.Sprintf("厂内报告样号 %s 与留样样号 %s 不一致", p.SampleNo, unit.SampleNo))
		}
	}
	out.Comparable = out.UnitOK && out.BasisOK && out.SamplingOK && out.RelationOK
	out.MetalDeltaPPM = s.MetalPPM - p.MetalPPM
	out.MoistureDeltaPPM = s.MoisturePPM - p.MoisturePPM
	if out.Comparable {
		out.WithinTolerance = abs64(out.MetalDeltaPPM) <= c.MetalTolerancePPM &&
			abs64(out.MoistureDeltaPPM) <= c.MoistureTolerancePPM
		if !out.WithinTolerance {
			out.DifferenceSources = append(out.DifferenceSources,
				fmt.Sprintf("超出约定误差范围：金属差 %d ppm（容差 %d），水分差 %d ppm（容差 %d）",
					out.MetalDeltaPPM, c.MetalTolerancePPM, out.MoistureDeltaPPM, c.MoistureTolerancePPM))
		} else {
			out.DifferenceSources = append(out.DifferenceSources,
				fmt.Sprintf("在约定误差范围内（金属差 %d ppm ≤ %d，水分差 %d ppm ≤ %d）；按约定选用一方结果，不取平均",
					out.MetalDeltaPPM, c.MetalTolerancePPM, out.MoistureDeltaPPM, c.MoistureTolerancePPM))
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// 开案
// ---------------------------------------------------------------------------

type OpenCaseInput struct {
	SampleID             int64  `json:"sample_id"`
	SampleUnitID         int64  `json:"sample_unit_id"`
	SamePosition         bool   `json:"same_position"`
	MetalTolerancePPM    int64  `json:"metal_tolerance_ppm"`
	MoistureTolerancePPM int64  `json:"moisture_tolerance_ppm"`
	Note                 string `json:"note"`
}

// OpenReviewCase opens a dispute on exactly one truck and freezes only that
// truck's payment. Other trucks of the same supplier keep paying.
func (a *App) OpenReviewCase(ctx context.Context, actor User, tripID int64, in OpenCaseInput) (ReviewCase, error) {
	if err := RequireRole(actor, "quality", "lab"); err != nil {
		return ReviewCase{}, err
	}
	if in.MetalTolerancePPM == 0 {
		in.MetalTolerancePPM = 2_000
	}
	if in.MoistureTolerancePPM == 0 {
		in.MoistureTolerancePPM = 2_000
	}
	if in.MetalTolerancePPM < 0 || in.MoistureTolerancePPM < 0 {
		return ReviewCase{}, invalid("约定误差范围不能为负")
	}
	var caseID int64
	err := a.DB.Tx(ctx, func(tx pgx.Tx) error {
		var supplierID int64
		var batchNo string
		if err := tx.QueryRow(ctx, `select supplier_id, batch_no from rt.trips where id=$1`, tripID).
			Scan(&supplierID, &batchNo); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return notFound("trip %d", tripID)
			}
			return err
		}
		var sampleNo string
		var sampleTrip int64
		sampleID := in.SampleID
		if sampleID == 0 {
			err := tx.QueryRow(ctx, `select id, sample_no from rt.samples where trip_id=$1 order by id limit 1`, tripID).
				Scan(&sampleID, &sampleNo)
			if err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return blocked("车次 %d 没有封样，无法建立复核", tripID)
				}
				return err
			}
		} else {
			if err := tx.QueryRow(ctx, `select sample_no, trip_id from rt.samples where id=$1`, sampleID).
				Scan(&sampleNo, &sampleTrip); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return notFound("sample %d", sampleID)
				}
				return err
			}
			if sampleTrip != tripID {
				return invalid("封样 %d 不属于车次 %d：复核必须围绕同一车材料", sampleID, tripID)
			}
		}
		var unitID *int64
		if in.SampleUnitID > 0 {
			var owner int64
			if err := tx.QueryRow(ctx, `select sample_id from rt.sample_units where id=$1`, in.SampleUnitID).Scan(&owner); err != nil {
				return notFound("sample unit %d", in.SampleUnitID)
			}
			if owner != sampleID {
				return invalid("留样不属于该封样")
			}
			unitID = &in.SampleUnitID
		}
		var seq int
		if err := tx.QueryRow(ctx, `select count(*)+1 from rt.review_cases where trip_id=$1`, tripID).Scan(&seq); err != nil {
			return err
		}
		caseNo := fmt.Sprintf("RC-%s-%02d", batchNo, seq)
		err := tx.QueryRow(ctx, `insert into rt.review_cases(case_no, trip_id, supplier_id, sample_id, sample_unit_id,
			status, same_position, metal_tolerance_ppm, moisture_tolerance_ppm, opened_by, note)
			values($1,$2,$3,$4,$5,'open',$6,$7,$8,$9,$10) returning id`,
			caseNo, tripID, supplierID, sampleID, unitID, in.SamePosition, in.MetalTolerancePPM,
			in.MoistureTolerancePPM, actor.ID, in.Note).Scan(&caseID)
		if err != nil {
			return err
		}
		// Freeze only this truck.
		if _, err := tx.Exec(ctx, `insert into rt.payment_freezes(case_id, trip_id, reason, frozen_by)
			values($1,$2,$3,$4) on conflict do nothing`, caseID, tripID,
			"复核争议：仅冻结本车付款，不影响同供应商其他车", actor.ID); err != nil {
			return err
		}
		return insertAudit(ctx, tx, actor, "review.open", "review_case", caseID, map[string]any{
			"trip_id": tripID, "sample_id": sampleID})
	})
	if err != nil {
		return ReviewCase{}, err
	}
	return a.GetReviewCase(ctx, actor, caseID)
}

// ---------------------------------------------------------------------------
// 双方报告
// ---------------------------------------------------------------------------

type PartyReportInput struct {
	Party          string `json:"party"`
	ReportNo       string `json:"report_no"`
	LabName        string `json:"lab_name"`
	SampleNo       string `json:"sample_no"`
	SealNo         string `json:"seal_no"`
	SplitFrom      string `json:"split_from"`
	Position       string `json:"position"`
	DeclaredUnit   string `json:"declared_unit"`
	MoisturePPM    int64  `json:"moisture_ppm"`
	ImpurityPPM    *int64 `json:"impurity_ppm"`
	ImpurityBasis  string `json:"impurity_basis"`
	MetalPPM       int64  `json:"metal_ppm"`
	MetalBasis     string `json:"metal_basis"`
	QuoteCentsPerT int64  `json:"quote_cents_per_t"`
}

// AddPartyReport records a supplier or plant result on the case. A report that
// arrives after the case was decided never overwrites the decision; it opens a
// re-review request and bumps the evidence version.
func (a *App) AddPartyReport(ctx context.Context, actor User, caseID int64, in PartyReportInput) (ReviewCase, error) {
	if err := RequireRole(actor, "quality", "lab", "purchasing"); err != nil {
		return ReviewCase{}, err
	}
	if in.Party != "supplier" && in.Party != "plant" {
		return ReviewCase{}, invalid("party 必须是 supplier 或 plant")
	}
	if in.ReportNo == "" {
		return ReviewCase{}, invalid("报告编号 report_no 必填")
	}
	unit, unitOK := normalizeRatioUnit(in.DeclaredUnit)
	if in.MetalPPM <= 0 || in.MetalPPM > 1_000_000 {
		return ReviewCase{}, invalid("metal_ppm 必须在 (0,1000000]")
	}
	if in.MoisturePPM < 0 || in.MoisturePPM >= 1_000_000 {
		return ReviewCase{}, invalid("moisture_ppm 必须在 [0,1000000)")
	}
	if in.MetalBasis != "dry" && in.MetalBasis != "wet" {
		return ReviewCase{}, invalid("金属基准 metal_basis 必须显式声明 dry 或 wet")
	}
	if in.ImpurityBasis != "dry" && in.ImpurityBasis != "wet" {
		return ReviewCase{}, invalid("杂物基准 impurity_basis 必须显式声明 dry 或 wet")
	}
	err := a.DB.Tx(ctx, func(tx pgx.Tx) error {
		var caseTrip int64
		var caseStatus string
		var evVersion int
		if err := tx.QueryRow(ctx, `select trip_id, status, evidence_version from rt.review_cases where id=$1 for update`,
			caseID).Scan(&caseTrip, &caseStatus, &evVersion); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return notFound("review case %d", caseID)
			}
			return err
		}
		if _, err := tx.Exec(ctx, `insert into rt.party_reports(case_id, party, report_no, lab_name, sample_no, seal_no,
			split_from, position, declared_unit, unit_ok, moisture_ppm, impurity_ppm, impurity_basis, metal_ppm,
			metal_basis, quote_cents_per_t, created_by)
			values($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)`,
			caseID, in.Party, in.ReportNo, in.LabName, in.SampleNo, in.SealNo, in.SplitFrom, in.Position,
			unit, unitOK, in.MoisturePPM, in.ImpurityPPM, in.ImpurityBasis, in.MetalPPM, in.MetalBasis,
			in.QuoteCentsPerT, actor.ID); err != nil {
			return conflict("同一案件同一方报告编号 %q 已存在", in.ReportNo)
		}
		newVersion := evVersion + 1
		if _, err := tx.Exec(ctx, `update rt.review_cases set evidence_version=$2,
			status=case when status in ('adjudicated','settled') then status else 'comparing' end where id=$1`,
			caseID, newVersion); err != nil {
			return err
		}
		if caseStatus == CaseAdjudicated || caseStatus == CaseSettled {
			if err := ensureRereview(ctx, tx, caseID, actor, "裁定后新增"+partyLabel(in.Party)+"报告，不能覆盖旧决定，需重新复核"); err != nil {
				return err
			}
		}
		return insertAudit(ctx, tx, actor, "review.party_report", "review_case", caseID, map[string]any{
			"party": in.Party, "report_no": in.ReportNo, "evidence_version": newVersion})
	})
	if err != nil {
		return ReviewCase{}, err
	}
	return a.GetReviewCase(ctx, actor, caseID)
}

func partyLabel(p string) string {
	if p == "supplier" {
		return "供应商"
	}
	return "厂内"
}

func ensureRereview(ctx context.Context, tx pgx.Tx, caseID int64, actor User, reason string) error {
	var exists bool
	if err := tx.QueryRow(ctx, `select exists(select 1 from rt.rereview_requests where case_id=$1 and status='open')`,
		caseID).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return nil
	}
	var adjID *int64
	_ = tx.QueryRow(ctx, `select id from rt.adjudications where case_id=$1 and status='effective'
		order by decision_seq desc limit 1`, caseID).Scan(&adjID)
	_, err := tx.Exec(ctx, `insert into rt.rereview_requests(case_id, adjudication_id, reason, requested_by)
		values($1,$2,$3,$4)`, caseID, adjID, reason, actor.ID)
	return err
}

// ---------------------------------------------------------------------------
// 外送盲样
// ---------------------------------------------------------------------------

type ExternalReturnInput struct {
	BlindCode     string `json:"blind_code"`
	LabName       string `json:"lab_name"`
	SampleNo      string `json:"sample_no"`
	SealNo        string `json:"seal_no"`
	Position      string `json:"position"`
	DeclaredUnit  string `json:"declared_unit"`
	MoisturePPM   int64  `json:"moisture_ppm"`
	ImpurityPPM   *int64 `json:"impurity_ppm"`
	ImpurityBasis string `json:"impurity_basis"`
	MetalPPM      int64  `json:"metal_ppm"`
	MetalBasis    string `json:"metal_basis"`
	Note          string `json:"note"`
}

// ReturnExternalReport accepts a lab result keyed only by blind code. It never
// carries the supplier quote. If the returned seal/sample does not trace to
// the drawn retained sample, the report is routed to pending evidence instead
// of being compared.
func (a *App) ReturnExternalReport(ctx context.Context, actor User, in ExternalReturnInput) (ReviewCase, error) {
	if err := RequireRole(actor, "lab", "quality"); err != nil {
		return ReviewCase{}, err
	}
	if in.BlindCode == "" {
		return ReviewCase{}, invalid("盲编号 blind_code 必填：外送结果只能按盲编号关联")
	}
	unit, unitOK := normalizeRatioUnit(in.DeclaredUnit)
	err := a.DB.Tx(ctx, func(tx pgx.Tx) error {
		var extID, caseID int64
		var unitID *int64
		var status, caseStatus string
		var evVersion int
		if err := tx.QueryRow(ctx, `select e.id, e.case_id, e.sample_unit_id, e.status, c.status, c.evidence_version
			from rt.external_reports e join rt.review_cases c on c.id=e.case_id
			where e.blind_code=$1 for update of e, c`, in.BlindCode).
			Scan(&extID, &caseID, &unitID, &status, &caseStatus, &evVersion); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return notFound("盲编号 %q 未登记领取", in.BlindCode)
			}
			return err
		}
		if status != "awaiting" {
			return conflict("盲编号 %q 已返回，不能重复登记", in.BlindCode)
		}
		match := "linked"
		note := in.Note
		if unitID != nil {
			var unitSample, unitSeal, unitPos string
			if err := tx.QueryRow(ctx, `select s.sample_no, u.seal_no, u.position from rt.sample_units u
				join rt.samples s on s.id=u.sample_id where u.id=$1`, *unitID).
				Scan(&unitSample, &unitSeal, &unitPos); err != nil {
				return err
			}
			if in.SealNo != "" && in.SealNo != unitSeal {
				match = "pending_evidence"
				note = appendNote(note, fmt.Sprintf("返回封签 %s 与留样封签 %s 不一致，转待补证", in.SealNo, unitSeal))
			}
			if in.SampleNo != "" && in.SampleNo != unitSample {
				match = "pending_evidence"
				note = appendNote(note, fmt.Sprintf("返回样号 %s 与留样样号 %s 不一致，转待补证", in.SampleNo, unitSample))
			}
			_ = unitPos
		} else {
			match = "pending_evidence"
			note = appendNote(note, "外送领取缺少留样关联，原封签/分样关系不明，转待补证")
		}
		newStatus := "returned"
		if match != "linked" {
			newStatus = "pending_evidence"
		}
		if _, err := tx.Exec(ctx, `update rt.external_reports set lab_name=case when $2='' then lab_name else $2 end,
			status=$3, match_status=$4, declared_unit=$5, unit_ok=$6, moisture_ppm=$7, impurity_ppm=$8,
			impurity_basis=$9, metal_ppm=$10, metal_basis=$11, position=$12, sample_no=$13, seal_no=$14,
			received_at=now(), note=$15 where id=$1`,
			extID, in.LabName, newStatus, match, unit, unitOK, in.MoisturePPM, in.ImpurityPPM,
			in.ImpurityBasis, in.MetalPPM, in.MetalBasis, in.Position, in.SampleNo, in.SealNo, note); err != nil {
			return err
		}
		newVersion := evVersion + 1
		if _, err := tx.Exec(ctx, `update rt.review_cases set evidence_version=$2,
			status=case when $3='pending_evidence' then 'pending_evidence'
			            when status in ('adjudicated','settled') then status else 'comparing' end where id=$1`,
			caseID, newVersion, match); err != nil {
			return err
		}
		if caseStatus == CaseAdjudicated || caseStatus == CaseSettled {
			if err := ensureRereview(ctx, tx, caseID, actor, "裁定后外送报告返回，不能覆盖旧决定，需重新复核"); err != nil {
				return err
			}
		}
		return insertAudit(ctx, tx, actor, "review.external_return", "external_report", extID, map[string]any{
			"blind_code": in.BlindCode, "match": match, "evidence_version": newVersion})
	})
	if err != nil {
		return ReviewCase{}, err
	}
	var caseID int64
	if err := a.DB.Pool.QueryRow(ctx, `select case_id from rt.external_reports where blind_code=$1`, in.BlindCode).Scan(&caseID); err != nil {
		return ReviewCase{}, err
	}
	return a.GetReviewCase(ctx, actor, caseID)
}

func appendNote(note, add string) string {
	if note == "" {
		return add
	}
	return note + "；" + add
}

// RevealExternalReport formally links a blind result to its retained sample
// and exposes it for comparison. Until this step the numeric result is masked.
func (a *App) RevealExternalReport(ctx context.Context, actor User, extID int64, note string) (ReviewCase, error) {
	if err := RequireRole(actor, "quality"); err != nil {
		return ReviewCase{}, err
	}
	var caseID int64
	err := a.DB.Tx(ctx, func(tx pgx.Tx) error {
		var status, match, caseStatus string
		var evVersion int
		if err := tx.QueryRow(ctx, `select e.status, e.match_status, e.case_id, c.status, c.evidence_version
			from rt.external_reports e join rt.review_cases c on c.id=e.case_id where e.id=$1 for update of e, c`,
			extID).Scan(&status, &match, &caseID, &caseStatus, &evVersion); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return notFound("external report %d", extID)
			}
			return err
		}
		if status == "awaiting" {
			return conflict("外送结果尚未返回，不能揭示")
		}
		if match == "pending_evidence" {
			return blocked("原封签或分样关系不明，须先补证再揭示")
		}
		if status == "revealed" {
			return conflict("该外送结果已揭示")
		}
		if _, err := tx.Exec(ctx, `update rt.external_reports set status='revealed', revealed=true,
			revealed_by=$2, revealed_at=now(), note=case when $3='' then note else $3 end where id=$1`,
			extID, actor.ID, note); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update rt.review_cases set evidence_version=$2,
			status=case when status in ('adjudicated','settled') then status else 'comparing' end where id=$1`,
			caseID, evVersion+1); err != nil {
			return err
		}
		return insertAudit(ctx, tx, actor, "review.external_reveal", "external_report", extID, map[string]any{
			"case_id": caseID})
	})
	if err != nil {
		return ReviewCase{}, err
	}
	return a.GetReviewCase(ctx, actor, caseID)
}

// ---------------------------------------------------------------------------
// 读取
// ---------------------------------------------------------------------------

func (a *App) GetReviewCase(ctx context.Context, actor User, caseID int64) (ReviewCase, error) {
	var c ReviewCase
	err := a.DB.Pool.QueryRow(ctx, `select c.id, c.case_no, c.trip_id, t.batch_no, c.supplier_id, s.name,
		c.sample_id, sm.sample_no, c.sample_unit_id, c.status, c.same_position, c.metal_tolerance_ppm,
		c.moisture_tolerance_ppm, c.evidence_version, c.opened_at, c.note
		from rt.review_cases c join rt.trips t on t.id=c.trip_id join rt.suppliers s on s.id=c.supplier_id
		join rt.samples sm on sm.id=c.sample_id where c.id=$1`, caseID).
		Scan(&c.ID, &c.CaseNo, &c.TripID, &c.BatchNo, &c.SupplierID, &c.SupplierName, &c.SampleID, &c.SampleNo,
			&c.SampleUnitID, &c.Status, &c.SamePosition, &c.MetalTolerancePPM, &c.MoistureTolerancePPM,
			&c.EvidenceVersion, &c.OpenedAt, &c.Note)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return c, notFound("review case %d", caseID)
		}
		return c, err
	}
	// Party reports. Supplier quote is only shown to quality/finance/admin.
	canSeeQuote := actor.Role == "quality" || actor.Role == "finance" || actor.Role == "admin" || actor.Role == "purchasing"
	c.PartyReports = []PartyReport{}
	rows, err := a.DB.Pool.Query(ctx, `select id, case_id, party, report_no, lab_name, sample_no, seal_no, split_from,
		position, declared_unit, unit_ok, moisture_ppm, impurity_ppm, impurity_basis, metal_ppm, metal_basis,
		quote_cents_per_t, relation_ok, received_at from rt.party_reports where case_id=$1 order by id`, caseID)
	if err != nil {
		return c, err
	}
	for rows.Next() {
		var r PartyReport
		if err := rows.Scan(&r.ID, &r.CaseID, &r.Party, &r.ReportNo, &r.LabName, &r.SampleNo, &r.SealNo,
			&r.SplitFrom, &r.Position, &r.DeclaredUnit, &r.UnitOK, &r.MoisturePPM, &r.ImpurityPPM,
			&r.ImpurityBasis, &r.MetalPPM, &r.MetalBasis, &r.QuoteCentsPerT, &r.RelationOK, &r.ReceivedAt); err != nil {
			rows.Close()
			return c, err
		}
		if !canSeeQuote {
			r.QuoteCentsPerT = 0
		}
		c.PartyReports = append(c.PartyReports, r)
	}
	rows.Close()

	// External reports: numeric result masked until formally revealed.
	c.ExternalReports = []ExternalReport{}
	erows, err := a.DB.Pool.Query(ctx, `select id, case_id, blind_code, lab_name, sample_unit_id, status,
		match_status, declared_unit, unit_ok, moisture_ppm, impurity_ppm, impurity_basis, metal_ppm, metal_basis,
		position, sample_no, seal_no, revealed, received_at, note
		from rt.external_reports where case_id=$1 order by id`, caseID)
	if err != nil {
		return c, err
	}
	for erows.Next() {
		var e ExternalReport
		if err := erows.Scan(&e.ID, &e.CaseID, &e.BlindCode, &e.LabName, &e.SampleUnitID, &e.Status,
			&e.MatchStatus, &e.DeclaredUnit, &e.UnitOK, &e.MoisturePPM, &e.ImpurityPPM, &e.ImpurityBasis,
			&e.MetalPPM, &e.MetalBasis, &e.Position, &e.SampleNo, &e.SealNo, &e.Revealed, &e.ReceivedAt, &e.Note); err != nil {
			erows.Close()
			return c, err
		}
		if !e.Revealed {
			// Before reveal the platform does not surface the lab's numbers or any
			// quote; only the blind code and status are visible.
			e.Masked = true
			e.MoisturePPM, e.ImpurityPPM, e.MetalPPM = 0, nil, 0
			e.SampleNo, e.SealNo, e.Position = "", "", ""
		}
		c.ExternalReports = append(c.ExternalReports, e)
	}
	erows.Close()

	// Adjudications.
	c.Adjudications = []Adjudication{}
	arows, err := a.DB.Pool.Query(ctx, `select id, case_id, decision_seq, adopted, party_report_id, external_report_id,
		scope_trip_ids, comparable, within_tolerance, difference_sources, reason, evidence_version, status,
		adopted_assay_ids, decided_by, decided_at from rt.adjudications where case_id=$1 order by decision_seq`, caseID)
	if err != nil {
		return c, err
	}
	for arows.Next() {
		var ad Adjudication
		var diffs []byte
		if err := arows.Scan(&ad.ID, &ad.CaseID, &ad.DecisionSeq, &ad.Adopted, &ad.PartyReportID, &ad.ExternalReportID,
			&ad.ScopeTripIDs, &ad.Comparable, &ad.WithinTolerance, &diffs, &ad.Reason, &ad.EvidenceVersion,
			&ad.Status, &ad.AdoptedAssayIDs, &ad.DecidedBy, &ad.DecidedAt); err != nil {
			arows.Close()
			return c, err
		}
		_ = json.Unmarshal(diffs, &ad.DifferenceSources)
		if ad.DifferenceSources == nil {
			ad.DifferenceSources = []string{}
		}
		c.Adjudications = append(c.Adjudications, ad)
	}
	arows.Close()
	for i := range c.Adjudications {
		lines, err := a.adjudicationLines(ctx, c.Adjudications[i].ID)
		if err != nil {
			return c, err
		}
		c.Adjudications[i].Lines = lines
	}

	// Freeze (only this truck).
	var fz PaymentFreeze
	err = a.DB.Pool.QueryRow(ctx, `select id, case_id, trip_id, reason, status, frozen_at, released_at
		from rt.payment_freezes where case_id=$1 and status='active' order by id desc limit 1`, caseID).
		Scan(&fz.ID, &fz.CaseID, &fz.TripID, &fz.Reason, &fz.Status, &fz.FrozenAt, &fz.ReleasedAt)
	if err == nil {
		c.Freeze = &fz
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return c, err
	}
	var rr RereviewRequest
	err = a.DB.Pool.QueryRow(ctx, `select id, case_id, adjudication_id, reason, status, requested_at
		from rt.rereview_requests where case_id=$1 and status='open' order by id desc limit 1`, caseID).
		Scan(&rr.ID, &rr.CaseID, &rr.AdjudicationID, &rr.Reason, &rr.Status, &rr.RequestedAt)
	if err == nil {
		c.OpenRereview = &rr
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return c, err
	}

	// Comparability against the latest report per party.
	var s, p *PartyReport
	for i := range c.PartyReports {
		r := &c.PartyReports[i]
		if r.Party == "supplier" {
			s = r
		} else if r.Party == "plant" {
			p = r
		}
	}
	var unit *SampleUnit
	if c.SampleUnitID != nil {
		u, err := a.GetSampleUnit(ctx, *c.SampleUnitID)
		if err == nil {
			unit = &u
		}
	}
	cmp := evaluateComparability(&c, s, p, unit)
	c.Comparability = &cmp
	return c, nil
}

func (a *App) adjudicationLines(ctx context.Context, adjID int64) ([]AdjudicationLine, error) {
	rows, err := a.DB.Pool.Query(ctx, `select l.trip_id, t.batch_no, l.old_amount_cents, l.new_amount_cents,
		l.delta_cents, l.settlement, l.payable_id, l.note from rt.adjudication_lines l
		join rt.trips t on t.id=l.trip_id where l.adjudication_id=$1 order by l.trip_id`, adjID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AdjudicationLine{}
	for rows.Next() {
		var l AdjudicationLine
		if err := rows.Scan(&l.TripID, &l.BatchNo, &l.OldAmountCents, &l.NewAmountCents, &l.DeltaCents,
			&l.Settlement, &l.PayableID, &l.Note); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

type CaseListItem struct {
	ID              int64     `json:"id"`
	CaseNo          string    `json:"case_no"`
	TripID          int64     `json:"trip_id"`
	BatchNo         string    `json:"batch_no"`
	SupplierName    string    `json:"supplier_name"`
	Status          string    `json:"status"`
	EvidenceVersion int       `json:"evidence_version"`
	Frozen          bool      `json:"frozen"`
	OpenRereview    bool      `json:"open_rereview"`
	OpenedAt        time.Time `json:"opened_at"`
	Note            string    `json:"note"`
}

func (a *App) ListReviewCases(ctx context.Context) ([]CaseListItem, error) {
	rows, err := a.DB.Pool.Query(ctx, `select c.id, c.case_no, c.trip_id, t.batch_no, s.name, c.status,
		c.evidence_version, c.opened_at, c.note,
		exists(select 1 from rt.payment_freezes f where f.case_id=c.id and f.status='active'),
		exists(select 1 from rt.rereview_requests r where r.case_id=c.id and r.status='open')
		from rt.review_cases c join rt.trips t on t.id=c.trip_id join rt.suppliers s on s.id=c.supplier_id
		order by c.id desc`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CaseListItem{}
	for rows.Next() {
		var i CaseListItem
		if err := rows.Scan(&i.ID, &i.CaseNo, &i.TripID, &i.BatchNo, &i.SupplierName, &i.Status,
			&i.EvidenceVersion, &i.OpenedAt, &i.Note, &i.Frozen, &i.OpenRereview); err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// 试算
// ---------------------------------------------------------------------------

type LotImpact struct {
	LotID      int64  `json:"lot_id"`
	LotKey     string `json:"lot_key"`
	PileName   string `json:"pile_name"`
	PileKind   string `json:"pile_kind"`
	WeightG    int64  `json:"weight_g"`
	OldMetalMg int64  `json:"old_metal_mg"`
	NewMetalMg int64  `json:"new_metal_mg"`
}

type TrialCandidate struct {
	Source           string      `json:"source"`
	PartyReportID    *int64      `json:"party_report_id,omitempty"`
	ExternalReportID *int64      `json:"external_report_id,omitempty"`
	Label            string      `json:"label"`
	MetalPPM         int64       `json:"metal_ppm"`
	MoisturePPM      int64       `json:"moisture_ppm"`
	MetalBasis       string      `json:"metal_basis"`
	AmountCents      int64       `json:"amount_cents"`
	DeltaCents       int64       `json:"delta_cents"`
	MetalMg          int64       `json:"metal_mg"`
	LotImpacts       []LotImpact `json:"lot_impacts"`
	Blocked          string      `json:"blocked,omitempty"`
}

type CaseTrial struct {
	CaseID             int64            `json:"case_id"`
	CaseNo             string           `json:"case_no"`
	TripID             int64            `json:"trip_id"`
	BatchNo            string           `json:"batch_no"`
	Comparability      Comparability    `json:"comparability"`
	CurrentAmountCents int64            `json:"current_amount_cents"`
	CurrentStatus      string           `json:"current_status"`
	Candidates         []TrialCandidate `json:"candidates"`
	AveragingForbidden bool             `json:"averaging_forbidden"`
	Note               string           `json:"note"`
}

// TrialReview computes, without persisting, what each candidate result would
// pay and how the mixed pile's metal would move. It deliberately produces no
// averaged candidate.
func (a *App) TrialReview(ctx context.Context, caseID int64) (CaseTrial, error) {
	out := CaseTrial{AveragingForbidden: true, Candidates: []TrialCandidate{}}
	var s, p *PartyReport
	// Reuse GetReviewCase with a system-ish actor to get latest reports.
	c, err := a.GetReviewCase(ctx, User{Role: "admin"}, caseID)
	if err != nil {
		return out, err
	}
	out.CaseID, out.CaseNo, out.TripID, out.BatchNo = c.ID, c.CaseNo, c.TripID, c.BatchNo
	if c.Comparability != nil {
		out.Comparability = *c.Comparability
	}
	for i := range c.PartyReports {
		switch c.PartyReports[i].Party {
		case "supplier":
			s = &c.PartyReports[i]
		case "plant":
			p = &c.PartyReports[i]
		}
	}
	var curID int64
	if err := a.DB.Pool.QueryRow(ctx, `select id from rt.payables where trip_id=$1 and is_current`, c.TripID).Scan(&curID); err == nil {
		if pay, err := a.GetPayable(ctx, curID); err == nil {
			out.CurrentAmountCents, out.CurrentStatus = pay.AmountCents, pay.Status
		}
	}
	cands := []struct {
		src   string
		label string
		party *PartyReport
		ext   *ExternalReport
	}{}
	if s != nil {
		cands = append(cands, struct {
			src, label string
			party      *PartyReport
			ext        *ExternalReport
		}{"supplier", "供应商报告", s, nil})
	}
	if p != nil {
		cands = append(cands, struct {
			src, label string
			party      *PartyReport
			ext        *ExternalReport
		}{"plant", "厂内报告", p, nil})
	}
	for i := range c.ExternalReports {
		e := &c.ExternalReports[i]
		if e.Revealed {
			cands = append(cands, struct {
				src, label string
				party      *PartyReport
				ext        *ExternalReport
			}{"external", "外送检测（已揭示）", nil, e})
		}
	}
	for _, cand := range cands {
		tc := TrialCandidate{Source: cand.src, Label: cand.label}
		var assay num.AssayInput
		if cand.party != nil {
			id := cand.party.ID
			tc.PartyReportID = &id
			tc.MetalPPM, tc.MoisturePPM, tc.MetalBasis = cand.party.MetalPPM, cand.party.MoisturePPM, cand.party.MetalBasis
			assay = num.AssayInput{MoisturePPM: cand.party.MoisturePPM, ImpurityPPM: cand.party.ImpurityPPM,
				ImpurityBasis: cand.party.ImpurityBasis, MetalPPM: cand.party.MetalPPM, MetalBasis: cand.party.MetalBasis}
		} else if cand.ext != nil {
			id := cand.ext.ID
			tc.ExternalReportID = &id
			tc.MetalPPM, tc.MoisturePPM, tc.MetalBasis = cand.ext.MetalPPM, cand.ext.MoisturePPM, cand.ext.MetalBasis
			assay = num.AssayInput{MoisturePPM: cand.ext.MoisturePPM, ImpurityPPM: cand.ext.ImpurityPPM,
				ImpurityBasis: cand.ext.ImpurityBasis, MetalPPM: cand.ext.MetalPPM, MetalBasis: cand.ext.MetalBasis}
		}
		calc, err := a.priceTripWith(ctx, a.DB.Pool, c.TripID, &assay)
		if err != nil {
			return out, err
		}
		if !calc.CanPay {
			tc.Blocked = calc.BlockedReason
		} else {
			tc.AmountCents = calc.Result.AmountCents
			tc.MetalMg = calc.Result.MetalMg
			tc.DeltaCents = tc.AmountCents - out.CurrentAmountCents
			impacts, err := a.trialLotImpact(ctx, c.TripID, calc.Result.MetalMg)
			if err != nil {
				return out, err
			}
			tc.LotImpacts = impacts
		}
		out.Candidates = append(out.Candidates, tc)
	}
	if !out.Comparability.Comparable {
		out.Note = "条件不可比：只列差异来源与各自结果，不生成平均/折中值，也不能自动择高或择低"
	} else if out.Comparability.WithinTolerance {
		out.Note = "在约定误差范围内：可任选一方结果，但必须明确采用哪一方，不取平均"
	} else {
		out.Note = "超出约定误差范围：需说明差异来源并由质量人员明确采用哪一方结果"
	}
	return out, nil
}

// trialLotImpact previews how each live lot holding this trip's material would
// change if its total metal became newTotalMg. It mirrors ApplyReassay's
// proportional split but persists nothing.
func (a *App) trialLotImpact(ctx context.Context, tripID, newTotalMg int64) ([]LotImpact, error) {
	rows, err := a.DB.Pool.Query(ctx, `select o.lot_id, o.metal_mg, l.lot_key, p.name, p.kind, l.weight_g
		from rt.lot_origins o join rt.inventory_lots l on l.id=o.lot_id join rt.piles p on p.id=l.pile_id
		where o.trip_id=$1 and o.weight_g > 0 and l.weight_g > 0 order by o.lot_id`, tripID)
	if err != nil {
		return nil, err
	}
	type row struct {
		lotID           int64
		key, pile, kind string
		weight, old     int64
	}
	list := []row{}
	olds := []int64{}
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.lotID, &r.old, &r.key, &r.pile, &r.kind, &r.weight); err != nil {
			rows.Close()
			return nil, err
		}
		list = append(list, r)
		olds = append(olds, r.old)
	}
	rows.Close()
	if len(list) == 0 {
		return []LotImpact{}, nil
	}
	newVals := num.Alloc(newTotalMg, olds)
	out := make([]LotImpact, 0, len(list))
	for i, r := range list {
		out = append(out, LotImpact{LotID: r.lotID, LotKey: r.key, PileName: r.pile, PileKind: r.kind,
			WeightG: r.weight, OldMetalMg: r.old, NewMetalMg: newVals[i]})
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// 裁定与结算
// ---------------------------------------------------------------------------

type AdjudicateInput struct {
	ExpectedEvidenceVersion int     `json:"expected_evidence_version"`
	Adopted                 string  `json:"adopted"`
	PartyReportID           int64   `json:"party_report_id"`
	ExternalReportID        int64   `json:"external_report_id"`
	ScopeTripIDs            []int64 `json:"scope_trip_ids"`
	Reason                  string  `json:"reason"`
	AcknowledgeIncomparable bool    `json:"acknowledge_incomparable"`
}

// Adjudicate records the determination, changes the effective assay, adjusts
// the mixed pile in place, and settles the payable according to whether it was
// already paid. It refuses to proceed if the evidence changed since the
// approver looked at it, and it never derives an average.
func (a *App) Adjudicate(ctx context.Context, actor User, caseID int64, in AdjudicateInput) (ReviewCase, error) {
	if err := RequireRole(actor, "quality"); err != nil {
		return ReviewCase{}, err
	}
	switch in.Adopted {
	case "supplier", "plant", "external", "none":
	default:
		return ReviewCase{}, invalid("adopted 必须是 supplier / plant / external / none")
	}
	if in.Reason == "" {
		return ReviewCase{}, invalid("裁定必须写明理由 reason")
	}
	err := a.DB.Tx(ctx, func(tx pgx.Tx) error {
		var tripID, supplierID, sampleID int64
		var status string
		var evVersion int
		var tolMetal, tolMoisture int64
		var samePos bool
		if err := tx.QueryRow(ctx, `select trip_id, supplier_id, sample_id, status, evidence_version,
			metal_tolerance_ppm, moisture_tolerance_ppm, same_position from rt.review_cases where id=$1 for update`,
			caseID).Scan(&tripID, &supplierID, &sampleID, &status, &evVersion, &tolMetal, &tolMoisture, &samePos); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return notFound("review case %d", caseID)
			}
			return err
		}
		if evVersion != in.ExpectedEvidenceVersion {
			return blocked("证据已变化（当前版本 %d，你查看的是 %d）：请重新查看最新报告后再裁定", evVersion, in.ExpectedEvidenceVersion)
		}
		var openRereview bool
		if err := tx.QueryRow(ctx, `select exists(select 1 from rt.rereview_requests where case_id=$1 and status='open')`,
			caseID).Scan(&openRereview); err != nil {
			return err
		}
		if (status == CaseAdjudicated || status == CaseSettled) && !openRereview {
			return conflict("案件已裁定，新报告需先提出重新复核，不能覆盖旧决定")
		}
		// Scope: only this truck's material carries this sample. Other trucks are
		// explicitly rejected rather than dragged along.
		for _, id := range in.ScopeTripIDs {
			if id != tripID {
				return blocked("采用范围只能包含本车 %d：车次 %d 没有同一封样，不能套用本裁定，需单独开案（不拖住其他车）", tripID, id)
			}
		}
		scope := []int64{tripID}

		// Load latest reports and evaluate current comparability.
		rep, err := a.loadCaseReports(ctx, tx, caseID)
		if err != nil {
			return err
		}
		var unit *SampleUnit
		var unitID *int64
		if err := tx.QueryRow(ctx, `select sample_unit_id from rt.review_cases where id=$1`, caseID).Scan(&unitID); err != nil {
			return err
		}
		if unitID != nil {
			u, err := a.getSampleUnitTx(ctx, tx, *unitID)
			if err != nil {
				return err
			}
			unit = &u
		}
		cmpCase := &ReviewCase{MetalTolerancePPM: tolMetal, MoistureTolerancePPM: tolMoisture, SamePosition: samePos}
		cmp := evaluateComparability(cmpCase, rep.supplier, rep.plant, unit)

		var chosenAssay num.AssayInput
		var partyReportID, extReportID *int64
		// The approver must name the exact report being adopted. If they name one,
		// it is used verbatim; otherwise the latest report of that party is used.
		var named *PartyReport
		if in.PartyReportID > 0 {
			nr, err := a.findPartyReport(ctx, tx, caseID, in.PartyReportID)
			if err != nil {
				return err
			}
			named = nr
		}
		switch in.Adopted {
		case "supplier", "plant":
			want := in.Adopted
			var pick *PartyReport
			if named != nil {
				if named.Party != want {
					return invalid("所选报告属于 %s，与采用方 %s 不一致", named.Party, want)
				}
				pick = named
			} else if want == "supplier" {
				pick = rep.supplier
			} else {
				pick = rep.plant
			}
			if pick == nil {
				return invalid("尚无%s报告，不能采用该方结果", partyLabel(want))
			}
			if !cmp.Comparable && !in.AcknowledgeIncomparable {
				return blocked("条件不可比：%s；如仍要采用需明确承认不可比并写明理由", strings.Join(cmp.DifferenceSources, "；"))
			}
			chosenAssay = partyAssay(pick)
			id := pick.ID
			partyReportID = &id
		case "external":
			pick := rep.external
			if in.ExternalReportID > 0 {
				er, err := a.findExternalReport(ctx, tx, caseID, in.ExternalReportID)
				if err != nil {
					return err
				}
				pick = er
			}
			if pick == nil {
				return invalid("尚无已揭示的外送报告，不能采用外送结果")
			}
			if !pick.Revealed {
				return blocked("外送结果 %s 尚未正式揭示，不能采用", pick.BlindCode)
			}
			if pick.MatchStatus == "pending_evidence" {
				return blocked("外送报告原封签/分样关系不明，须补证后才能采用")
			}
			chosenAssay = externalAssay(pick)
			id := pick.ID
			extReportID = &id
		case "none":
			// Record the determination without adopting any result. The truck stays
			// frozen and pending evidence.
		}
		// Supersede the previous effective determination (kept in history).
		if _, err := tx.Exec(ctx, `update rt.adjudications set status='superseded' where case_id=$1 and status='effective'`,
			caseID); err != nil {
			return err
		}
		var seq int
		if err := tx.QueryRow(ctx, `select coalesce(max(decision_seq),0)+1 from rt.adjudications where case_id=$1`,
			caseID).Scan(&seq); err != nil {
			return err
		}
		diffs, _ := json.Marshal(cmp.DifferenceSources)
		var adjID int64
		if err := tx.QueryRow(ctx, `insert into rt.adjudications(case_id, decision_seq, adopted, party_report_id,
			external_report_id, scope_trip_ids, comparable, within_tolerance, difference_sources, reason,
			evidence_version, status, adopted_assay_ids, decided_by)
			values($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,'effective','{}',$12) returning id`,
			caseID, seq, in.Adopted, partyReportID, extReportID, scope, cmp.Comparable, cmp.WithinTolerance,
			diffs, in.Reason, evVersion, actor.ID).Scan(&adjID); err != nil {
			return err
		}
		if in.Adopted == "none" {
			if _, err := tx.Exec(ctx, `update rt.review_cases set status='pending_evidence' where id=$1`, caseID); err != nil {
				return err
			}
			if err := insertAudit(ctx, tx, actor, "review.adjudicate", "review_case", caseID, map[string]any{
				"adjudication_id": adjID, "adopted": "none"}); err != nil {
				return err
			}
			return nil
		}

		// Adopt the chosen result as a new assay version on the sealed sample.
		var version int
		if err := tx.QueryRow(ctx, `select coalesce(max(version),0)+1 from rt.assay_results where sample_id=$1`,
			sampleID).Scan(&version); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update rt.assay_results set quality_status='superseded'
			where sample_id=$1 and quality_status in ('pending','adopted')`, sampleID); err != nil {
			return err
		}
		var assayID int64
		if err := tx.QueryRow(ctx, `insert into rt.assay_results(sample_id, version, moisture_ppm, impurity_ppm,
			impurity_basis, metal_ppm, metal_basis, inspector, quality_status, quality_by, quality_at, quality_note,
			source, adjudication_id)
			values($1,$2,$3,$4,$5,$6,$7,$8,'adopted',$9,now(),$10,'adjudication',$11) returning id`,
			sampleID, version, chosenAssay.MoisturePPM, chosenAssay.ImpurityPPM, chosenAssay.ImpurityBasis,
			chosenAssay.MetalPPM, chosenAssay.MetalBasis, "差异裁定", actor.ID, in.Reason, adjID).Scan(&assayID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update rt.assay_results ar set quality_status='superseded'
			where ar.quality_status='adopted' and ar.id<>$2
			  and ar.sample_id in (select id from rt.samples where trip_id=$1)`, tripID, assayID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `insert into rt.trip_assay_adoptions(trip_id, assay_id, adopted_by, note)
			values($1,$2,$3,$4)
			on conflict (trip_id) do update set assay_id=excluded.assay_id, adopted_by=excluded.adopted_by,
			adopted_at=now(), note=excluded.note`, tripID, assayID, actor.ID, "差异裁定采用："+in.Reason); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update rt.adjudications set adopted_assay_ids=$2 where id=$1`,
			adjID, []int64{assayID}); err != nil {
			return err
		}
		// Propagate to the mixed pile in place; absence of stock is not fatal.
		if _, err := a.applyReassayTx(ctx, tx, actor, tripID); err != nil {
			if !errors.Is(err, ErrBlocked) {
				return err
			}
		}

		// Old payable state before settlement.
		var oldID *int64
		var oldAmount int64
		var oldStatus string
		err = tx.QueryRow(ctx, `select id, amount_cents, status from rt.payables
			where trip_id=$1 and is_current for update`, tripID).Scan(&oldID, &oldAmount, &oldStatus)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		line := AdjudicationLine{TripID: tripID, OldAmountCents: oldAmount, Settlement: "pending"}
		if oldID != nil && oldStatus == "paid" {
			// Already paid: the delta becomes a recovery/追补 entry; the paid
			// version itself stays untouched.
			calc, err := a.priceTripWith(ctx, tx, tripID, &chosenAssay)
			if err != nil {
				return err
			}
			if !calc.CanPay {
				return blocked("采用结果无法计算应付：%s", calc.BlockedReason)
			}
			// A re-review supersedes an earlier determination, so the recovery must
			// be incremental: this decision's amount minus everything already
			// settled on this truck, otherwise a second determination double counts.
			var priorSettled int64
			if err := tx.QueryRow(ctx, `select coalesce(sum(delta_cents),0) from rt.payment_recoveries
				where trip_id=$1 and kind='recovery'`, tripID).Scan(&priorSettled); err != nil {
				return err
			}
			effectiveTarget := oldAmount + priorSettled
			delta := calc.Result.AmountCents - effectiveTarget
			line.NewAmountCents = calc.Result.AmountCents
			line.DeltaCents = delta
			line.PayableID = oldID
			line.Settlement = "recovery"
			line.Note = "已付款按差额形成追补/追回，原付款版本保留"
			if priorSettled != 0 {
				line.Note = "重新复核：按本决定金额与已结算累计额之差计算增量追补/追回"
			}
			if _, err := tx.Exec(ctx, `insert into rt.payment_recoveries(case_id, adjudication_id, trip_id, kind,
				delta_cents, base_payable_id, note, created_by) values($1,$2,$3,'recovery',$4,$5,$6,$7)`,
				caseID, adjID, tripID, delta, *oldID, line.Note, actor.ID); err != nil {
				return err
			}
		} else {
			// Not yet paid: rebuild the payment basis as a new payable version.
			newID, err := a.computePayableTx(ctx, tx, actor, tripID, "差异裁定后重建付款依据")
			if err != nil {
				if errors.Is(err, ErrBlocked) {
					line.Settlement = "blocked"
					line.Note = "尚未付款，但当前参数不足以重建付款依据：" + err.Error()
				} else {
					return err
				}
			} else {
				line.Settlement = "rebuild"
				line.PayableID = &newID
				line.Note = "尚未付款，已按裁定结果重建付款依据（新版本）"
				pay, err := a.getPayableTx(ctx, tx, newID)
				if err != nil {
					return err
				}
				line.NewAmountCents = pay.AmountCents
				line.DeltaCents = pay.AmountCents - oldAmount
				if _, err := tx.Exec(ctx, `insert into rt.payment_recoveries(case_id, adjudication_id, trip_id, kind,
					delta_cents, base_payable_id, new_payable_id, note, created_by)
					values($1,$2,$3,'rebuild',$4,$5,$6,$7,$8)`,
					caseID, adjID, tripID, line.DeltaCents, oldID, newID, line.Note, actor.ID); err != nil {
					return err
				}
			}
		}
		if _, err := tx.Exec(ctx, `insert into rt.adjudication_lines(adjudication_id, trip_id, old_amount_cents,
			new_amount_cents, delta_cents, settlement, payable_id, note) values($1,$2,$3,$4,$5,$6,$7,$8)`,
			adjID, tripID, line.OldAmountCents, line.NewAmountCents, line.DeltaCents, line.Settlement,
			line.PayableID, line.Note); err != nil {
			return err
		}
		// Release this truck's freeze only when no other dispute on the SAME truck
		// is still open. Other trucks were never frozen and are untouched.
		var stillOpen int
		if err := tx.QueryRow(ctx, `select count(*) from rt.review_cases
			where trip_id=$1 and id<>$2 and status in ('open','comparing','pending_evidence')`,
			tripID, caseID).Scan(&stillOpen); err != nil {
			return err
		}
		if stillOpen == 0 {
			if _, err := tx.Exec(ctx, `update rt.payment_freezes set status='released', released_at=now()
				where trip_id=$1 and status='active'`, tripID); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `update rt.rereview_requests set status='closed', closed_at=now()
			where case_id=$1 and status='open'`, caseID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update rt.review_cases set status='settled' where id=$1`, caseID); err != nil {
			return err
		}
		return insertAudit(ctx, tx, actor, "review.adjudicate", "review_case", caseID, map[string]any{
			"adjudication_id": adjID, "adopted": in.Adopted, "amount_cents": line.NewAmountCents,
			"settlement": line.Settlement, "assay_id": assayID})
	})
	if err != nil {
		return ReviewCase{}, err
	}
	return a.GetReviewCase(ctx, actor, caseID)
}

type loadedReports struct {
	supplier *PartyReport
	plant    *PartyReport
	external *ExternalReport
}

func (a *App) loadCaseReports(ctx context.Context, q querier, caseID int64) (loadedReports, error) {
	var out loadedReports
	rows, err := q.Query(ctx, `select id, case_id, party, report_no, lab_name, sample_no, seal_no, split_from,
		position, declared_unit, unit_ok, moisture_ppm, impurity_ppm, impurity_basis, metal_ppm, metal_basis,
		quote_cents_per_t, relation_ok, received_at from rt.party_reports where case_id=$1 order by id`, caseID)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var r PartyReport
		if err := rows.Scan(&r.ID, &r.CaseID, &r.Party, &r.ReportNo, &r.LabName, &r.SampleNo, &r.SealNo,
			&r.SplitFrom, &r.Position, &r.DeclaredUnit, &r.UnitOK, &r.MoisturePPM, &r.ImpurityPPM,
			&r.ImpurityBasis, &r.MetalPPM, &r.MetalBasis, &r.QuoteCentsPerT, &r.RelationOK, &r.ReceivedAt); err != nil {
			rows.Close()
			return out, err
		}
		cp := r
		switch r.Party {
		case "supplier":
			out.supplier = &cp
		case "plant":
			out.plant = &cp
		}
	}
	rows.Close()
	var e ExternalReport
	err = q.QueryRow(ctx, `select id, case_id, blind_code, lab_name, sample_unit_id, status, match_status,
		declared_unit, unit_ok, moisture_ppm, impurity_ppm, impurity_basis, metal_ppm, metal_basis, position,
		sample_no, seal_no, revealed, received_at, note from rt.external_reports
		where case_id=$1 and status in ('returned','revealed') order by id desc limit 1`, caseID).
		Scan(&e.ID, &e.CaseID, &e.BlindCode, &e.LabName, &e.SampleUnitID, &e.Status, &e.MatchStatus,
			&e.DeclaredUnit, &e.UnitOK, &e.MoisturePPM, &e.ImpurityPPM, &e.ImpurityBasis, &e.MetalPPM,
			&e.MetalBasis, &e.Position, &e.SampleNo, &e.SealNo, &e.Revealed, &e.ReceivedAt, &e.Note)
	if err == nil {
		out.external = &e
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return out, err
	}
	return out, nil
}

func (a *App) findPartyReport(ctx context.Context, q querier, caseID, id int64) (*PartyReport, error) {
	var r PartyReport
	err := q.QueryRow(ctx, `select id, case_id, party, report_no, lab_name, sample_no, seal_no, split_from,
		position, declared_unit, unit_ok, moisture_ppm, impurity_ppm, impurity_basis, metal_ppm, metal_basis,
		quote_cents_per_t, relation_ok, received_at from rt.party_reports where id=$1 and case_id=$2`, id, caseID).
		Scan(&r.ID, &r.CaseID, &r.Party, &r.ReportNo, &r.LabName, &r.SampleNo, &r.SealNo, &r.SplitFrom,
			&r.Position, &r.DeclaredUnit, &r.UnitOK, &r.MoisturePPM, &r.ImpurityPPM, &r.ImpurityBasis,
			&r.MetalPPM, &r.MetalBasis, &r.QuoteCentsPerT, &r.RelationOK, &r.ReceivedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, notFound("party report %d on case %d", id, caseID)
		}
		return nil, err
	}
	return &r, nil
}

func (a *App) findExternalReport(ctx context.Context, q querier, caseID, id int64) (*ExternalReport, error) {
	var e ExternalReport
	err := q.QueryRow(ctx, `select id, case_id, blind_code, lab_name, sample_unit_id, status, match_status,
		declared_unit, unit_ok, moisture_ppm, impurity_ppm, impurity_basis, metal_ppm, metal_basis, position,
		sample_no, seal_no, revealed, received_at, note from rt.external_reports where id=$1 and case_id=$2`,
		id, caseID).
		Scan(&e.ID, &e.CaseID, &e.BlindCode, &e.LabName, &e.SampleUnitID, &e.Status, &e.MatchStatus,
			&e.DeclaredUnit, &e.UnitOK, &e.MoisturePPM, &e.ImpurityPPM, &e.ImpurityBasis, &e.MetalPPM,
			&e.MetalBasis, &e.Position, &e.SampleNo, &e.SealNo, &e.Revealed, &e.ReceivedAt, &e.Note)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, notFound("external report %d on case %d", id, caseID)
		}
		return nil, err
	}
	return &e, nil
}

func partyAssay(r *PartyReport) num.AssayInput {
	return num.AssayInput{MoisturePPM: r.MoisturePPM, ImpurityPPM: r.ImpurityPPM,
		ImpurityBasis: r.ImpurityBasis, MetalPPM: r.MetalPPM, MetalBasis: r.MetalBasis}
}

func externalAssay(e *ExternalReport) num.AssayInput {
	return num.AssayInput{MoisturePPM: e.MoisturePPM, ImpurityPPM: e.ImpurityPPM,
		ImpurityBasis: e.ImpurityBasis, MetalPPM: e.MetalPPM, MetalBasis: e.MetalBasis}
}

func (a *App) getSampleUnitTx(ctx context.Context, q querier, id int64) (SampleUnit, error) {
	var u SampleUnit
	err := q.QueryRow(ctx, `select u.id, u.unit_no, u.sample_id, s.sample_no, s.trip_id, t.batch_no,
		u.parent_unit_id, u.origin, u.position, u.seal_no, u.initial_g, u.remaining_g, u.status, u.note, u.created_at
		from rt.sample_units u join rt.samples s on s.id=u.sample_id join rt.trips t on t.id=s.trip_id
		where u.id=$1`, id).
		Scan(&u.ID, &u.UnitNo, &u.SampleID, &u.SampleNo, &u.TripID, &u.BatchNo, &u.ParentUnitID, &u.Origin,
			&u.Position, &u.SealNo, &u.InitialG, &u.RemainingG, &u.Status, &u.Note, &u.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return u, notFound("sample unit %d", id)
		}
		return u, err
	}
	return u, nil
}

// getPayableTx reads a payable inside a transaction after it was just written.
func (a *App) getPayableTx(ctx context.Context, q querier, id int64) (Payable, error) {
	var p Payable
	err := q.QueryRow(ctx, `select id, trip_id, version, is_current, wet_net_g, metal_mg, amount_cents, status
		from rt.payables where id=$1`, id).
		Scan(&p.ID, &p.TripID, &p.Version, &p.IsCurrent, &p.WetNetG, &p.MetalMg, &p.AmountCents, &p.Status)
	if err != nil {
		return p, err
	}
	return p, nil
}

// activeFreeze returns the active dispute freeze for a trip, if any.
func activeFreeze(ctx context.Context, q querier, tripID int64) (bool, string, error) {
	var reason string
	err := q.QueryRow(ctx, `select reason from rt.payment_freezes where trip_id=$1 and status='active'
		order by id desc limit 1`, tripID).Scan(&reason)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, "", nil
		}
		return false, "", err
	}
	return true, reason, nil
}

// ListRecoveries exposes the settled differences for audit.
type Recovery struct {
	ID             int64     `json:"id"`
	CaseID         int64     `json:"case_id"`
	AdjudicationID int64     `json:"adjudication_id"`
	TripID         int64     `json:"trip_id"`
	BatchNo        string    `json:"batch_no"`
	Kind           string    `json:"kind"`
	DeltaCents     int64     `json:"delta_cents"`
	BasePayableID  *int64    `json:"base_payable_id"`
	NewPayableID   *int64    `json:"new_payable_id"`
	Note           string    `json:"note"`
	CreatedAt      time.Time `json:"created_at"`
}

func (a *App) ListRecoveries(ctx context.Context) ([]Recovery, error) {
	rows, err := a.DB.Pool.Query(ctx, `select r.id, r.case_id, r.adjudication_id, r.trip_id, t.batch_no, r.kind,
		r.delta_cents, r.base_payable_id, r.new_payable_id, r.note, r.created_at
		from rt.payment_recoveries r join rt.trips t on t.id=r.trip_id order by r.id desc`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Recovery{}
	for rows.Next() {
		var r Recovery
		if err := rows.Scan(&r.ID, &r.CaseID, &r.AdjudicationID, &r.TripID, &r.BatchNo, &r.Kind, &r.DeltaCents,
			&r.BasePayableID, &r.NewPayableID, &r.Note, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
