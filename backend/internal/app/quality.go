package app

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

type Sample struct {
	ID        int64     `json:"id"`
	SampleNo  string    `json:"sample_no"`
	TripID    int64     `json:"trip_id"`
	Position  string    `json:"position"`
	SealNo    string    `json:"seal_no"`
	SealedAt  time.Time `json:"sealed_at"`
	SealedBy  *int64    `json:"sealed_by"`
	Note      string    `json:"note"`
	CreatedAt time.Time `json:"created_at"`
}

type Assay struct {
	ID            int64      `json:"id"`
	SampleID      int64      `json:"sample_id"`
	SampleNo      string     `json:"sample_no"`
	TripID        int64      `json:"trip_id"`
	Version       int        `json:"version"`
	MoisturePPM   int64      `json:"moisture_ppm"`
	ImpurityPPM   *int64     `json:"impurity_ppm"`
	ImpurityBasis string     `json:"impurity_basis"`
	MetalPPM      int64      `json:"metal_ppm"`
	MetalBasis    string     `json:"metal_basis"`
	Inspector     string     `json:"inspector"`
	SubmittedAt   time.Time  `json:"submitted_at"`
	QualityStatus string     `json:"quality_status"`
	QualityBy     *int64     `json:"quality_by"`
	QualityAt     *time.Time `json:"quality_at"`
	QualityNote   string     `json:"quality_note"`
}

type CreateSampleInput struct {
	SampleNo string    `json:"sample_no"`
	Position string    `json:"position"`
	SealNo   string    `json:"seal_no"`
	SealedAt time.Time `json:"sealed_at"`
	Note     string    `json:"note"`
}

// CreateSample binds a sealed sample directly to a trip and a sampling
// position. It rejects a sample that lacks either, so no unowned assay can be
// auto-assigned by supplier+date later.
func (a *App) CreateSample(ctx context.Context, actor User, tripID int64, in CreateSampleInput) (Sample, error) {
	if err := RequireRole(actor, "lab", "yard"); err != nil {
		return Sample{}, err
	}
	if in.Position == "" {
		return Sample{}, invalid("取样位置 position is required: a seal must bind to a trip and a position")
	}
	if in.SealNo == "" {
		return Sample{}, invalid("封样号 seal_no is required")
	}
	if in.SampleNo == "" {
		return Sample{}, invalid("sample_no is required")
	}
	if in.SealedAt.IsZero() {
		in.SealedAt = time.Now()
	}
	var out Sample
	err := a.DB.Tx(ctx, func(tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx, `select exists(select 1 from rt.trips where id=$1)`, tripID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return notFound("trip %d", tripID)
		}
		err := tx.QueryRow(ctx, `insert into rt.samples(sample_no, trip_id, position, seal_no, sealed_at, sealed_by, note)
			values($1,$2,$3,$4,$5,$6,$7)
			returning id, sample_no, trip_id, position, seal_no, sealed_at, sealed_by, note, created_at`,
			in.SampleNo, tripID, in.Position, in.SealNo, in.SealedAt, actor.ID, in.Note).
			Scan(&out.ID, &out.SampleNo, &out.TripID, &out.Position, &out.SealNo, &out.SealedAt, &out.SealedBy,
				&out.Note, &out.CreatedAt)
		if err != nil {
			return conflict("sample_no or seal already exists")
		}
		return insertAudit(ctx, tx, actor, "sample.create", "sample", out.ID, map[string]any{
			"trip_id": tripID, "position": in.Position, "seal_no": in.SealNo})
	})
	return out, err
}

// AddAssay stores an inspector's result. It never becomes effective on its own:
// quality must adopt it. Submitting a new version supersedes the previous one
// but leaves it in history.
func (a *App) AddAssay(ctx context.Context, actor User, sampleID int64, in Assay) (Assay, error) {
	if err := RequireRole(actor, "lab"); err != nil {
		return Assay{}, err
	}
	if in.MoisturePPM < 0 || in.MoisturePPM >= 1_000_000 {
		return Assay{}, invalid("moisture_ppm must be in [0,1000000)")
	}
	if in.MetalPPM <= 0 || in.MetalPPM > 1_000_000 {
		return Assay{}, invalid("metal_ppm must be in (0,1000000]")
	}
	if in.MetalBasis != "dry" && in.MetalBasis != "wet" {
		return Assay{}, invalid("metal_basis must be dry or wet")
	}
	if in.ImpurityBasis != "" && in.ImpurityBasis != "dry" && in.ImpurityBasis != "wet" {
		return Assay{}, invalid("impurity_basis must be dry or wet")
	}
	if in.ImpurityPPM != nil && (*in.ImpurityPPM < 0 || *in.ImpurityPPM >= 1_000_000) {
		return Assay{}, invalid("impurity_ppm must be in [0,1000000)")
	}
	if in.Inspector == "" {
		in.Inspector = actor.DisplayName
	}
	var out Assay
	err := a.DB.Tx(ctx, func(tx pgx.Tx) error {
		var sampleExists bool
		if err := tx.QueryRow(ctx, `select exists(select 1 from rt.samples where id=$1)`, sampleID).Scan(&sampleExists); err != nil {
			return err
		}
		if !sampleExists {
			return notFound("sample %d", sampleID)
		}
		var version int
		if err := tx.QueryRow(ctx, `select coalesce(max(version),0)+1 from rt.assay_results where sample_id=$1`,
			sampleID).Scan(&version); err != nil {
			return err
		}
		// A new inspector submission supersedes the previous *pending* one. An
		// already-adopted result stays effective until quality adopts the new
		// version, so a lab re-test never changes the payable state by itself.
		if _, err := tx.Exec(ctx, `update rt.assay_results set quality_status='superseded'
			where sample_id=$1 and quality_status='pending'`, sampleID); err != nil {
			return err
		}
		err := tx.QueryRow(ctx, `insert into rt.assay_results(sample_id, version, moisture_ppm, impurity_ppm,
			impurity_basis, metal_ppm, metal_basis, inspector) values($1,$2,$3,$4,$5,$6,$7,$8)
			returning id, sample_id, version, moisture_ppm, impurity_ppm, impurity_basis, metal_ppm, metal_basis,
			inspector, submitted_at, quality_status, quality_by, quality_at, quality_note`,
			sampleID, version, in.MoisturePPM, in.ImpurityPPM, in.ImpurityBasis, in.MetalPPM, in.MetalBasis, in.Inspector).
			Scan(&out.ID, &out.SampleID, &out.Version, &out.MoisturePPM, &out.ImpurityPPM, &out.ImpurityBasis,
				&out.MetalPPM, &out.MetalBasis, &out.Inspector, &out.SubmittedAt, &out.QualityStatus,
				&out.QualityBy, &out.QualityAt, &out.QualityNote)
		if err != nil {
			return err
		}
		return insertAudit(ctx, tx, actor, "assay.submit", "assay", out.ID, map[string]any{
			"sample_id": sampleID, "version": version, "moisture_ppm": in.MoisturePPM, "metal_ppm": in.MetalPPM})
	})
	if err != nil {
		return Assay{}, err
	}
	return a.GetAssay(ctx, out.ID)
}

func (a *App) GetAssay(ctx context.Context, id int64) (Assay, error) {
	var out Assay
	err := a.DB.Pool.QueryRow(ctx, `select ar.id, ar.sample_id, s.sample_no, s.trip_id, ar.version, ar.moisture_ppm,
		ar.impurity_ppm, coalesce(ar.impurity_basis,''), ar.metal_ppm, ar.metal_basis, ar.inspector, ar.submitted_at,
		ar.quality_status, ar.quality_by, ar.quality_at, ar.quality_note
		from rt.assay_results ar join rt.samples s on s.id=ar.sample_id where ar.id=$1`, id).
		Scan(&out.ID, &out.SampleID, &out.SampleNo, &out.TripID, &out.Version, &out.MoisturePPM, &out.ImpurityPPM,
			&out.ImpurityBasis, &out.MetalPPM, &out.MetalBasis, &out.Inspector, &out.SubmittedAt,
			&out.QualityStatus, &out.QualityBy, &out.QualityAt, &out.QualityNote)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return out, notFound("assay %d", id)
		}
		return out, err
	}
	return out, nil
}

func (a *App) ListSamplesForTrip(ctx context.Context, tripID int64) ([]Sample, []Assay, error) {
	rows, err := a.DB.Pool.Query(ctx, `select id, sample_no, trip_id, position, seal_no, sealed_at, sealed_by, note, created_at
		from rt.samples where trip_id=$1 order by id`, tripID)
	if err != nil {
		return nil, nil, err
	}
	samples := []Sample{}
	ids := []int64{}
	for rows.Next() {
		var s Sample
		if err := rows.Scan(&s.ID, &s.SampleNo, &s.TripID, &s.Position, &s.SealNo, &s.SealedAt, &s.SealedBy, &s.Note, &s.CreatedAt); err != nil {
			rows.Close()
			return nil, nil, err
		}
		samples = append(samples, s)
		ids = append(ids, s.ID)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	assays := []Assay{}
	if len(ids) == 0 {
		return samples, assays, nil
	}
	arows, err := a.DB.Pool.Query(ctx, `select ar.id from rt.assay_results ar
		where ar.sample_id = any($1) order by ar.sample_id, ar.version`, ids)
	if err != nil {
		return nil, nil, err
	}
	aids := []int64{}
	for arows.Next() {
		var id int64
		if err := arows.Scan(&id); err != nil {
			arows.Close()
			return nil, nil, err
		}
		aids = append(aids, id)
	}
	arows.Close()
	for _, id := range aids {
		as, err := a.GetAssay(ctx, id)
		if err != nil {
			return nil, nil, err
		}
		assays = append(assays, as)
	}
	return samples, assays, nil
}

// AdoptAssay is the quality gate: an inspector-submitted result becomes the
// effective assay only when quality adopts it. This is separate from payables:
// adoption alone does not change payable status.
func (a *App) AdoptAssay(ctx context.Context, actor User, assayID int64, note string) (Assay, error) {
	if err := RequireRole(actor, "quality"); err != nil {
		return Assay{}, err
	}
	err := a.DB.Tx(ctx, func(tx pgx.Tx) error {
		var sampleID, tripID int64
		var status string
		err := tx.QueryRow(ctx, `select ar.sample_id, s.trip_id, ar.quality_status
			from rt.assay_results ar join rt.samples s on s.id=ar.sample_id where ar.id=$1 for update of ar`,
			assayID).Scan(&sampleID, &tripID, &status)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return notFound("assay %d", assayID)
			}
			return err
		}
		if status != "pending" && status != "adopted" {
			return conflict("assay is %s and cannot be adopted", status)
		}
		if _, err := tx.Exec(ctx, `update rt.assay_results set quality_status='superseded'
			where sample_id=$1 and id<>$2 and quality_status='adopted'`, sampleID, assayID); err != nil {
			return err
		}
		// Any assay of another sample on the same trip that was previously adopted
		// is superseded too, so a trip has exactly one effective assay.
		if _, err := tx.Exec(ctx, `update rt.assay_results ar set quality_status='superseded'
			where ar.quality_status='adopted' and ar.id<>$2
			  and ar.sample_id in (select id from rt.samples where trip_id=$1)`, tripID, assayID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update rt.assay_results set quality_status='adopted', quality_by=$2,
			quality_at=now(), quality_note=$3 where id=$1`, assayID, actor.ID, note); err != nil {
			return err
		}
		// The adopted assay governs the whole trip: record it as the trip adoption.
		if _, err := tx.Exec(ctx, `insert into rt.trip_assay_adoptions(trip_id, assay_id, adopted_by, note)
			values($1,$2,$3,$4)
			on conflict (trip_id) do update set assay_id=excluded.assay_id, adopted_by=excluded.adopted_by,
			adopted_at=now(), note=excluded.note`, tripID, assayID, actor.ID, note); err != nil {
			return err
		}
		return insertAudit(ctx, tx, actor, "assay.adopt", "assay", assayID, map[string]any{
			"trip_id": tripID, "note": note})
	})
	if err != nil {
		return Assay{}, err
	}
	return a.GetAssay(ctx, assayID)
}

// RejectAssay marks a submitted result as rejected without touching payable.
func (a *App) RejectAssay(ctx context.Context, actor User, assayID int64, note string) (Assay, error) {
	if err := RequireRole(actor, "quality"); err != nil {
		return Assay{}, err
	}
	err := a.DB.Tx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `update rt.assay_results set quality_status='rejected', quality_by=$2,
			quality_at=now(), quality_note=$3 where id=$1 and quality_status in ('pending','adopted')`,
			assayID, actor.ID, note)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return conflict("assay cannot be rejected in its current state")
		}
		var tripID int64
		if err := tx.QueryRow(ctx, `select s.trip_id from rt.assay_results ar join rt.samples s on s.id=ar.sample_id where ar.id=$1`, assayID).Scan(&tripID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `delete from rt.trip_assay_adoptions where trip_id=$1 and assay_id=$2`, tripID, assayID); err != nil {
			return err
		}
		return insertAudit(ctx, tx, actor, "assay.reject", "assay", assayID, map[string]any{"note": note})
	})
	if err != nil {
		return Assay{}, err
	}
	return a.GetAssay(ctx, assayID)
}

// EffectiveAssay returns the trip's quality-adopted assay, or block error.
func effectiveAssay(ctx context.Context, q querier, tripID int64) (Assay, error) {
	var id int64
	err := q.QueryRow(ctx, `select assay_id from rt.trip_assay_adoptions where trip_id=$1`, tripID).Scan(&id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Assay{}, blocked("车次 %d 尚无质量确认采用的化验结果", tripID)
		}
		return Assay{}, err
	}
	var out Assay
	err = q.QueryRow(ctx, `select ar.id, ar.sample_id, s.sample_no, s.trip_id, ar.version, ar.moisture_ppm,
		ar.impurity_ppm, coalesce(ar.impurity_basis,''), ar.metal_ppm, ar.metal_basis, ar.inspector,
		ar.submitted_at, ar.quality_status, ar.quality_by, ar.quality_at, ar.quality_note
		from rt.assay_results ar join rt.samples s on s.id=ar.sample_id where ar.id=$1`, id).
		Scan(&out.ID, &out.SampleID, &out.SampleNo, &out.TripID, &out.Version, &out.MoisturePPM, &out.ImpurityPPM,
			&out.ImpurityBasis, &out.MetalPPM, &out.MetalBasis, &out.Inspector, &out.SubmittedAt,
			&out.QualityStatus, &out.QualityBy, &out.QualityAt, &out.QualityNote)
	if err != nil {
		return out, err
	}
	if out.QualityStatus != "adopted" {
		return out, blocked("化验结果 %d 当前状态 %s，不能用于付款", out.ID, out.QualityStatus)
	}
	return out, nil
}

// GradeApproval records the approved grade used for intake and pricing. Only an
// approved, non-isolated grade may enter a usable pile.
type GradeApproval struct {
	ID         int64      `json:"id"`
	TripID     int64      `json:"trip_id"`
	GradeCode  string     `json:"grade_code"`
	CoefPPM    int64      `json:"coef_ppm"`
	Isolated   bool       `json:"isolated"`
	Status     string     `json:"status"`
	ApprovedBy *int64     `json:"approved_by"`
	ApprovedAt *time.Time `json:"approved_at"`
	Note       string     `json:"note"`
}

func (a *App) ApproveGrade(ctx context.Context, actor User, tripID int64, gradeCode string, isolated bool, note string) (GradeApproval, error) {
	if err := RequireRole(actor, "quality"); err != nil {
		return GradeApproval{}, err
	}
	var out GradeApproval
	err := a.DB.Tx(ctx, func(tx pgx.Tx) error {
		var contractID int64
		if err := tx.QueryRow(ctx, `select contract_id from rt.trips where id=$1`, tripID).Scan(&contractID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return notFound("trip %d", tripID)
			}
			return err
		}
		var coef int64
		var allowed bool
		err := tx.QueryRow(ctx, `select coef_ppm, intake_allowed from rt.contract_grades where contract_id=$1 and grade_code=$2`,
			contractID, gradeCode).Scan(&coef, &allowed)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return invalid("grade %q is not defined on this contract", gradeCode)
			}
			return err
		}
		if !allowed {
			isolated = true
		}
		err = tx.QueryRow(ctx, `insert into rt.grade_approvals(trip_id, grade_code, coef_ppm, isolated, status,
			approved_by, approved_at, note) values($1,$2,$3,$4,'approved',$5,now(),$6)
			on conflict (trip_id) do update set grade_code=excluded.grade_code, coef_ppm=excluded.coef_ppm,
			isolated=excluded.isolated, status='approved', approved_by=excluded.approved_by,
			approved_at=now(), note=excluded.note, updated_at=now()
			returning id, trip_id, grade_code, coef_ppm, isolated, status, approved_by, approved_at, note`,
			tripID, gradeCode, coef, isolated, actor.ID, note).
			Scan(&out.ID, &out.TripID, &out.GradeCode, &out.CoefPPM, &out.Isolated, &out.Status,
				&out.ApprovedBy, &out.ApprovedAt, &out.Note)
		if err != nil {
			return err
		}
		return insertAudit(ctx, tx, actor, "grade.approve", "trip", tripID, map[string]any{
			"grade": gradeCode, "coef_ppm": coef, "isolated": isolated})
	})
	return out, err
}
