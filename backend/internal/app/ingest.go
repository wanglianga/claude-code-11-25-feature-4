package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// IngestScaleRecord is one weighing report row from the project's bundled
// scale-report files. It is never read from a live instrument.
type IngestScaleRecord struct {
	TripKey   string  `json:"trip_key"`
	Kind      string  `json:"kind"`
	WeightKg  float64 `json:"weight_kg"`
	TicketNo  string  `json:"ticket_no"`
	Device    string  `json:"device"`
	WeighedAt string  `json:"weighed_at"`
}

type IngestResult struct {
	Accepted  int          `json:"accepted"`
	Duplicate int          `json:"duplicate"`
	Rejected  int          `json:"rejected"`
	Items     []IngestItem `json:"items"`
}

type IngestItem struct {
	Index   int    `json:"index"`
	Ref     string `json:"ref"`
	Outcome string `json:"outcome"`
	ID      int64  `json:"id,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

// IngestScale accepts a whole scale-report file. A ticket already recorded is
// reported as a duplicate and not re-applied, so re-sending the same report is
// safe. Every record must name a known trip.
func (a *App) IngestScale(ctx context.Context, actor User, records []IngestScaleRecord) (IngestResult, error) {
	if err := RequireRole(actor, "yard", "purchasing"); err != nil {
		return IngestResult{}, err
	}
	out := IngestResult{Items: []IngestItem{}}
	for i, rec := range records {
		item := IngestItem{Index: i, Ref: rec.TicketNo}
		if rec.TripKey == "" || rec.TicketNo == "" {
			item.Outcome, item.Reason = "rejected", "trip_key and ticket_no required"
			out.Rejected++
			out.Items = append(out.Items, item)
			continue
		}
		var exists bool
		if err := a.DB.Pool.QueryRow(ctx, `select exists(select 1 from rt.weighings where ticket_no=$1)`, rec.TicketNo).Scan(&exists); err != nil {
			return out, err
		}
		if exists {
			item.Outcome = "duplicate"
			out.Duplicate++
			out.Items = append(out.Items, item)
			continue
		}
		var tripID int64
		err := a.DB.Pool.QueryRow(ctx, `select id from rt.trips where batch_no=$1`, rec.TripKey).Scan(&tripID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				item.Outcome, item.Reason = "rejected", fmt.Sprintf("unknown trip_key %q", rec.TripKey)
				out.Rejected++
				out.Items = append(out.Items, item)
				continue
			}
			return out, err
		}
		grams := int64(rec.WeightKg*1000 + 0.5)
		if grams <= 0 {
			item.Outcome, item.Reason = "rejected", "weight must be positive"
			out.Rejected++
			out.Items = append(out.Items, item)
			continue
		}
		at := time.Now()
		if rec.WeighedAt != "" {
			if t, err := time.Parse(time.RFC3339, rec.WeighedAt); err == nil {
				at = t
			}
		}
		w, err := a.AddWeighing(ctx, actor, tripID, Weighing{Kind: rec.Kind, WeightG: grams,
			TicketNo: rec.TicketNo, ScaleDevice: rec.Device, WeighedAt: at, Source: "scale-file"})
		if err != nil {
			if errors.Is(err, ErrConflict) {
				item.Outcome, item.Reason = "duplicate", err.Error()
				out.Duplicate++
			} else {
				item.Outcome, item.Reason = "rejected", err.Error()
				out.Rejected++
			}
			out.Items = append(out.Items, item)
			continue
		}
		item.Outcome, item.ID = "accepted", w.ID
		out.Accepted++
		out.Items = append(out.Items, item)
	}
	return out, nil
}

type IngestAssayRecord struct {
	TripKey       string   `json:"trip_key"`
	SampleNo      string   `json:"sample_no"`
	Position      string   `json:"position"`
	SealNo        string   `json:"seal_no"`
	MoisturePct   float64  `json:"moisture_pct"`
	ImpurityPct   *float64 `json:"impurity_pct"`
	ImpurityBasis string   `json:"impurity_basis"`
	MetalPct      float64  `json:"metal_pct"`
	MetalBasis    string   `json:"metal_basis"`
	Inspector     string   `json:"inspector"`
}

// IngestAssays accepts bundled sample-archive rows. A row without trip_key or
// position is rejected outright: an un-owned assay is never auto-assigned to a
// trip by supplier+date.
func (a *App) IngestAssays(ctx context.Context, actor User, records []IngestAssayRecord) (IngestResult, error) {
	if err := RequireRole(actor, "lab"); err != nil {
		return IngestResult{}, err
	}
	out := IngestResult{Items: []IngestItem{}}
	for i, rec := range records {
		item := IngestItem{Index: i, Ref: rec.SampleNo}
		if rec.TripKey == "" {
			item.Outcome = "rejected"
			item.Reason = "缺少 trip_key：封样与化验必须绑定车次，不按供应商+日期自动分配"
			out.Rejected++
			out.Items = append(out.Items, item)
			continue
		}
		if rec.Position == "" {
			item.Outcome = "rejected"
			item.Reason = "缺少 position：封样必须绑定取样位置"
			out.Rejected++
			out.Items = append(out.Items, item)
			continue
		}
		var tripID int64
		err := a.DB.Pool.QueryRow(ctx, `select id from rt.trips where batch_no=$1`, rec.TripKey).Scan(&tripID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				item.Outcome, item.Reason = "rejected", fmt.Sprintf("unknown trip_key %q", rec.TripKey)
				out.Rejected++
				out.Items = append(out.Items, item)
				continue
			}
			return out, err
		}
		// Create the sealed sample if the archive names a new one.
		var sampleID int64
		err = a.DB.Pool.QueryRow(ctx, `select id from rt.samples where sample_no=$1`, rec.SampleNo).Scan(&sampleID)
		if err != nil {
			if !errors.Is(err, pgx.ErrNoRows) {
				return out, err
			}
			s, err := a.CreateSample(ctx, actor, tripID, CreateSampleInput{SampleNo: rec.SampleNo,
				Position: rec.Position, SealNo: rec.SealNo, SealedAt: time.Now(), Note: "样品档案导入"})
			if err != nil {
				item.Outcome, item.Reason = "rejected", err.Error()
				out.Rejected++
				out.Items = append(out.Items, item)
				continue
			}
			sampleID = s.ID
		}
		imp := (*int64)(nil)
		if rec.ImpurityPct != nil {
			v := int64(*rec.ImpurityPct*10000 + 0.5)
			imp = &v
		}
		assay, err := a.AddAssay(ctx, actor, sampleID, Assay{
			MoisturePPM: int64(rec.MoisturePct*10000 + 0.5), ImpurityPPM: imp,
			ImpurityBasis: rec.ImpurityBasis, MetalPPM: int64(rec.MetalPct*10000 + 0.5),
			MetalBasis: rec.MetalBasis, Inspector: rec.Inspector,
		})
		if err != nil {
			item.Outcome, item.Reason = "rejected", err.Error()
			out.Rejected++
			out.Items = append(out.Items, item)
			continue
		}
		item.Outcome, item.ID = "accepted", assay.ID
		out.Accepted++
		out.Items = append(out.Items, item)
	}
	return out, nil
}
