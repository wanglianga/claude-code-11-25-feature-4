package app

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5"

	"recycletrace/backend/internal/num"
)

// Origin is a trip-level contribution carried by a lot.
type Origin struct {
	TripID      int64  `json:"trip_id"`
	BatchNo     string `json:"batch_no"`
	WeightG     int64  `json:"weight_g"`
	MetalMg     int64  `json:"metal_mg"`
	SharePPM    int64  `json:"share_ppm"`
	Basis       string `json:"basis"`
	RemainderG  int64  `json:"remainder_g"`
	RemainderMg int64  `json:"remainder_mg"`
}

type Lot struct {
	ID          int64    `json:"id"`
	Key         string   `json:"lot_key"`
	PileID      int64    `json:"pile_id"`
	PileName    string   `json:"pile_name"`
	PileKind    string   `json:"pile_kind"`
	TripID      *int64   `json:"trip_id"`
	BatchNo     string   `json:"batch_no"`
	GradeCode   string   `json:"grade_code"`
	WeightG     int64    `json:"weight_g"`
	MetalMg     int64    `json:"metal_mg"`
	MoisturePPM int64    `json:"moisture_ppm"`
	MetalKnown  bool     `json:"metal_known"`
	State       string   `json:"state"`
	Origins     []Origin `json:"origins"`
	CreatedAt   string   `json:"created_at"`
	UpdatedAt   string   `json:"updated_at"`
}

type TransferLine struct {
	LotID     int64  `json:"lot_id"`
	LotKey    string `json:"lot_key"`
	Direction string `json:"direction"`
	WeightG   int64  `json:"weight_g"`
	MetalMg   int64  `json:"metal_mg"`
	Basis     string `json:"basis"`
	LossClass string `json:"loss_class"`
	PileName  string `json:"pile_name"`
}

type Movement struct {
	ID          int64          `json:"id"`
	Kind        string         `json:"kind"`
	ExternalRef string         `json:"external_ref"`
	OccurredAt  string         `json:"occurred_at"`
	ActorID     int64          `json:"actor_id"`
	Note        string         `json:"note"`
	Lines       []TransferLine `json:"lines"`
	CreatedAt   string         `json:"created_at"`
}

// ------------------------------------------------------------------ helpers

type lotRow struct {
	ID       int64
	Key      string
	PileID   int64
	PileKind string
	WeightG  int64
	MetalMg  int64
	State    string
}

func lockLots(ctx context.Context, tx pgx.Tx, ids []int64) (map[int64]*lotRow, error) {
	sorted := append([]int64(nil), ids...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	out := map[int64]*lotRow{}
	var prev int64 = -1
	for _, id := range sorted {
		if id == prev {
			continue
		}
		prev = id
		var l lotRow
		err := tx.QueryRow(ctx, `select l.id, l.lot_key, l.pile_id, p.kind, l.weight_g, l.metal_mg, l.state
			from rt.inventory_lots l join rt.piles p on p.id=l.pile_id
			where l.id=$1 for update of l`, id).
			Scan(&l.ID, &l.Key, &l.PileID, &l.PileKind, &l.WeightG, &l.MetalMg, &l.State)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, notFound("批次 %d", id)
			}
			return nil, err
		}
		out[id] = &l
	}
	return out, nil
}

type originRow struct {
	TripID  int64
	WeightG int64
	MetalMg int64
	Basis   string
}

func lotOriginRows(ctx context.Context, q querier, lotID int64) ([]originRow, error) {
	rows, err := q.Query(ctx, `select trip_id, weight_g, metal_mg, basis from rt.lot_origins
		where lot_id=$1 and weight_g > 0 order by trip_id`, lotID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []originRow{}
	for rows.Next() {
		var o originRow
		if err := rows.Scan(&o.TripID, &o.WeightG, &o.MetalMg, &o.Basis); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

func pileKinds(ctx context.Context, q querier) (map[int64]string, error) {
	rows, err := q.Query(ctx, `select id, kind from rt.piles`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]string{}
	for rows.Next() {
		var id int64
		var kind string
		if err := rows.Scan(&id, &kind); err != nil {
			return nil, err
		}
		out[id] = kind
	}
	return out, rows.Err()
}

// gradeGuard: material may enter a usable pile only when every contributing
// trip has an approved, non-isolated grade. Isolated material can never become
// usable through mixing.
func gradeGuard(ctx context.Context, tx pgx.Tx, tripIDs []int64, destPileKind string) error {
	if destPileKind != "usable" {
		return nil
	}
	seen := map[int64]bool{}
	for _, tripID := range tripIDs {
		if seen[tripID] {
			continue
		}
		seen[tripID] = true
		var status, grade string
		var isolated bool
		err := tx.QueryRow(ctx, `select status, grade_code, isolated from rt.grade_approvals where trip_id=$1`, tripID).
			Scan(&status, &grade, &isolated)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return blocked("车次 %d 尚无质量确认的等级，不能进入可用堆", tripID)
			}
			return err
		}
		if status != "approved" {
			return blocked("车次 %d 等级状态 %s，不能进入可用堆", tripID, status)
		}
		if isolated {
			return blocked("车次 %d（等级 %s）为隔离料，不得通过混料进入可用堆", tripID, grade)
		}
	}
	return nil
}

// allocateMatrix splits rows across columns honouring both row and column
// margins exactly. The largest-remainder tail lands on a deterministic row and
// is reported back as residual so callers can attribute it.
// allocateMatrix splits row totals across columns. Row margins are exact; the
// column caps are respected. The second return value is each cell's deviation
// from the ideal floor split, i.e. the rounding remainder explicitly attributed
// to that cell — never silently dropped and never re-normalised later.
func allocateMatrix(rowTotals, colTotals []int64) ([][]int64, [][]int64) {
	rows := len(rowTotals)
	cols := len(colTotals)
	cells := make([][]int64, rows)
	resid := make([][]int64, rows)
	cap := append([]int64(nil), colTotals...)
	var colSum int64
	for _, c := range colTotals {
		colSum += c
	}
	for i := 0; i < rows; i++ {
		cells[i] = make([]int64, cols)
		resid[i] = make([]int64, cols)
		if rowTotals[i] <= 0 {
			continue
		}
		alloc := num.Alloc(rowTotals[i], cap)
		for j := range cap {
			cells[i][j] = alloc[j]
			cap[j] -= alloc[j]
			ideal := int64(0)
			if colSum > 0 {
				ideal = num.MulDiv(rowTotals[i], colTotals[j], colSum)
			}
			resid[i][j] = alloc[j] - ideal
		}
	}
	return cells, resid
}

// ------------------------------------------------------------------ intake

type IntakeInput struct {
	TripID     int64  `json:"trip_id"`
	PileID     int64  `json:"pile_id"`
	OccurredAt string `json:"occurred_at"`
	Note       string `json:"note"`
}

// Intake moves an approved trip's net weight into a pile as its own lot with a
// single origin (the trip itself). Usable piles require an approved grade.
func (a *App) Intake(ctx context.Context, actor User, in IntakeInput) (Lot, error) {
	if err := RequireRole(actor, "yard", "purchasing"); err != nil {
		return Lot{}, err
	}
	var lotID int64
	err := a.DB.Tx(ctx, func(tx pgx.Tx) error {
		adoption, err := currentAdoption(ctx, tx, in.TripID)
		if err != nil {
			return err
		}
		assay, err := effectiveAssay(ctx, tx, in.TripID)
		if err != nil {
			return err
		}
		var kinds map[int64]string
		kinds, err = pileKinds(ctx, tx)
		if err != nil {
			return err
		}
		pileKind, ok := kinds[in.PileID]
		if !ok {
			return invalid("未知堆位 %d", in.PileID)
		}
		if pileKind == "loss" || pileKind == "production" || pileKind == "shipped" {
			return invalid("堆位类型 %s 不接受来料入库", pileKind)
		}
		if err := gradeGuard(ctx, tx, []int64{in.TripID}, pileKind); err != nil {
			return err
		}
		var grade string
		var coef int64
		if err := tx.QueryRow(ctx, `select grade_code, coef_ppm from rt.grade_approvals where trip_id=$1`, in.TripID).
			Scan(&grade, &coef); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		// Inventory metal equals the contract's priced metal so the stock, the
		// payable and the trace all use one number.
		pricing, err := computeTripMetal(ctx, tx, in.TripID)
		if err != nil {
			return err
		}
		metalMg := pricing.MetalMg
		_ = assay
		var movID int64
		if err := tx.QueryRow(ctx, `insert into rt.movements(kind, external_ref, occurred_at, actor_id, note)
			values('inbound',$1, coalesce(nullif($2,'')::timestamptz, now()), $3, $4) returning id`,
			fmt.Sprintf("INTAKE-TRIP-%d", in.TripID), in.OccurredAt, actor.ID, in.Note).Scan(&movID); err != nil {
			return conflict("该车次已入库")
		}
		key := fmt.Sprintf("TRIP-%d", in.TripID)
		if err := tx.QueryRow(ctx, `insert into rt.inventory_lots(lot_key, pile_id, trip_id, grade_code, weight_g, metal_mg,
			moisture_ppm, metal_known) values($1,$2,$3,$4,$5,$6,$7,true) returning id`,
			key, in.PileID, in.TripID, grade, adoption.NetG, metalMg, assay.MoisturePPM).Scan(&lotID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `insert into rt.lot_origins(lot_id, trip_id, weight_g, metal_mg, share_ppm, basis)
			values($1,$2,$3,$4,1000000,'measured')`, lotID, in.TripID, adoption.NetG, metalMg); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `insert into rt.lot_events(lot_id, movement_id, event, weight_g, metal_mg, pile_id, occurred_at, detail)
			values($1,$2,'in',$3,$4,$5,now(),jsonb_build_object('kind','inbound'))`,
			lotID, movID, adoption.NetG, metalMg, in.PileID); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `insert into rt.transfer_lines(movement_id, lot_id, direction, weight_g, metal_mg, basis, note)
			values($1,$2,'in',$3,$4,'measured','来料入库')`, movID, lotID, adoption.NetG, metalMg)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `insert into rt.audit_log(actor_id, action, entity, entity_id, detail)
			values($1,'intake','lot',$2,$3)`, actor.ID, fmt.Sprint(lotID), map[string]any{
			"trip_id": in.TripID, "net_g": adoption.NetG, "metal_mg": metalMg, "pile_id": in.PileID})
		return err
	})
	if err != nil {
		return Lot{}, err
	}
	return a.GetLot(ctx, lotID)
}

// ------------------------------------------------------------------ transfer

type SourceLine struct {
	LotID      int64      `json:"lot_id"`
	WeightG    int64      `json:"weight_g"`
	DestPileID *int64     `json:"dest_pile_id"`
	Origins    []OriginIn `json:"origins"`
}

type OriginIn struct {
	TripID  int64 `json:"trip_id"`
	WeightG int64 `json:"weight_g"`
	MetalMg int64 `json:"metal_mg"`
}

type DestLine struct {
	PileID    int64  `json:"pile_id"`
	WeightG   int64  `json:"weight_g"`
	GradeCode string `json:"grade_code"`
}

type LossLine struct {
	WeightG   int64  `json:"weight_g"`
	LossClass string `json:"loss_class"`
	Note      string `json:"note"`
}

type TransferInput struct {
	ExternalRef  string       `json:"external_ref"`
	Kind         string       `json:"kind"`
	OccurredAt   string       `json:"occurred_at"`
	Mode         string       `json:"mode"`
	Note         string       `json:"note"`
	Sources      []SourceLine `json:"sources"`
	Destinations []DestLine   `json:"destinations"`
	Losses       []LossLine   `json:"losses"`
}

// Transfer is the only guarded path that moves material.
//
// Invariants enforced inside one transaction:
//   - replaying external_ref returns the original movement, never moving twice;
//   - every source lot is locked before its balance is read;
//   - out weight = in weight + declared loss, exactly;
//   - destination metal = source metal - process metal loss (drying loss keeps metal);
//   - every destination is a brand-new lot node, so returning material to the
//     same physical pile is a new state node and never a false cycle;
//   - a from->to link with quantity is written for every source/destination pair.
func (a *App) Transfer(ctx context.Context, actor User, in TransferInput) (Movement, error) {
	if in.ExternalRef == "" {
		return Movement{}, invalid("external_ref（转移单号）必填，用于幂等重放")
	}
	if in.Mode == "" {
		in.Mode = "uniform_share"
	}
	if in.Mode != "uniform_share" && in.Mode != "measured" && in.Mode != "unknown" {
		return Movement{}, invalid("mode 必须为 uniform_share / measured / unknown")
	}
	if len(in.Sources) == 0 || len(in.Destinations) == 0 {
		return Movement{}, invalid("转出与转入明细均不能为空")
	}
	if m, err := a.GetMovementByRef(ctx, in.ExternalRef); err == nil {
		return m, nil
	} else if !errors.Is(err, ErrNotFound) {
		return Movement{}, err
	}

	var movID int64
	err := a.DB.Tx(ctx, func(tx pgx.Tx) error {
		var dup int64
		err := tx.QueryRow(ctx, `select id from rt.movements where external_ref=$1`, in.ExternalRef).Scan(&dup)
		if err == nil {
			return conflict("转移单 %q 已应用", in.ExternalRef)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}

		ids := make([]int64, 0, len(in.Sources))
		for _, s := range in.Sources {
			ids = append(ids, s.LotID)
		}
		lots, err := lockLots(ctx, tx, ids)
		if err != nil {
			return err
		}
		kindByPile, err := pileKinds(ctx, tx)
		if err != nil {
			return err
		}

		var outW, lossW, inW int64
		srcW := make([]int64, len(in.Sources))
		srcM := make([]int64, len(in.Sources))
		for i, s := range in.Sources {
			if s.WeightG <= 0 {
				return invalid("转出重量必须为正")
			}
			lot := lots[s.LotID]
			if lot.State != "available" {
				return blocked("批次 %s 状态 %s，不可移动", lot.Key, lot.State)
			}
			if s.WeightG > lot.WeightG {
				return blocked("批次 %s 余额 %d g，不足以转出 %d g（禁止超库存）", lot.Key, lot.WeightG, s.WeightG)
			}
			outW += s.WeightG
			srcW[i] = s.WeightG
			if s.WeightG == lot.WeightG {
				srcM[i] = lot.MetalMg
			} else {
				srcM[i] = num.MulDiv(lot.MetalMg, s.WeightG, lot.WeightG)
			}
		}
		var outM int64
		for _, m := range srcM {
			outM += m
		}
		var processLoss int64
		for _, l := range in.Losses {
			if l.WeightG <= 0 {
				return invalid("损耗重量必须为正")
			}
			if l.LossClass != "drying" && l.LossClass != "process" {
				return invalid("损耗类别必须为 drying（干燥）或 process（加工），两者分别计量")
			}
			lossW += l.WeightG
			if l.LossClass == "process" {
				processLoss += l.WeightG
			}
		}
		destPiles := make([]int64, 0, len(in.Destinations))
		destWeights := make([]int64, 0, len(in.Destinations))
		destGrade := map[int64]string{}
		for _, d := range in.Destinations {
			if d.WeightG <= 0 {
				return invalid("转入重量必须为正")
			}
			if _, ok := kindByPile[d.PileID]; !ok {
				return invalid("未知堆位 %d", d.PileID)
			}
			destPiles = append(destPiles, d.PileID)
			destWeights = append(destWeights, d.WeightG)
			destGrade[d.PileID] = d.GradeCode
			inW += d.WeightG
		}
		if inW != outW-lossW {
			return invalid("转出 %d g - 明确损耗 %d g ≠ 转入 %d g，数量不齐先阻断", outW, lossW, inW)
		}

		// Loss split across sources. Drying removes water only (no metal);
		// process loss removes metal pro rata.
		wLoss := num.Alloc(lossW, srcW)
		processMetalLoss := int64(0)
		if processLoss > 0 {
			processMetalLoss = num.MulDiv(outM, processLoss, outW)
		}
		mLoss := num.Alloc(processMetalLoss, srcM)
		retW := make([]int64, len(in.Sources))
		retM := make([]int64, len(in.Sources))
		for i := range in.Sources {
			retW[i] = srcW[i] - wLoss[i]
			retM[i] = srcM[i] - mLoss[i]
			if retW[i] < 0 || retM[i] < 0 {
				return invalid("损耗分摊超过转出材料")
			}
		}
		retainedM := outM - processMetalLoss

		// Per-source trip composition, scaled for loss.
		type tripContrib struct {
			w, m  int64
			basis string
		}
		srcTrips := make([]map[int64]*tripContrib, len(in.Sources))
		srcLossTrips := make([]map[int64]*tripContrib, len(in.Sources))
		var explicit bool
		for i, s := range in.Sources {
			// dest_pile_id is only meaningful in measured (来源实测) mode. In
			// uniform_share / unknown the destinations block defines routing, and a
			// per-line dest would be silently ignored — reject it instead.
			if in.Mode == "measured" && s.DestPileID == nil {
				return invalid("measured（非均匀分拣）模式必须为每条转出行指定 dest_pile_id")
			}
			if in.Mode != "measured" && s.DestPileID != nil {
				return invalid("%s 模式下转出行不应指定 dest_pile_id；只有 measured 模式按来源实测路由", in.Mode)
			}
			if s.DestPileID != nil {
				if _, ok := kindByPile[*s.DestPileID]; !ok {
					return invalid("未知去向堆位 %d", *s.DestPileID)
				}
			}
			m := map[int64]*tripContrib{}
			if in.Mode == "measured" {
				if len(s.Origins) == 0 {
					return invalid("measured 模式需要为批次 %s 提供来源实测明细", lots[s.LotID].Key)
				}
				var ow int64
				for _, o := range s.Origins {
					if o.TripID == 0 || o.WeightG <= 0 {
						return invalid("来源实测明细的 trip_id 与重量必须有效")
					}
					ow += o.WeightG
					c := m[o.TripID]
					if c == nil {
						c = &tripContrib{basis: "measured"}
						m[o.TripID] = c
					}
					c.w += o.WeightG
					c.m += o.MetalMg
				}
				if ow != s.WeightG {
					return invalid("批次 %s 来源实测重量合计 %d g ≠ 转出 %d g", lots[s.LotID].Key, ow, s.WeightG)
				}
				explicit = true
			} else {
				orig, err := lotOriginRows(ctx, tx, s.LotID)
				if err != nil {
					return err
				}
				if len(orig) == 0 {
					return invalid("批次 %s 没有来源构成，无法拆分", lots[s.LotID].Key)
				}
				ws := make([]int64, len(orig))
				ms := make([]int64, len(orig))
				for k, o := range orig {
					ws[k] = o.WeightG
					ms[k] = o.MetalMg
				}
				aw := num.Alloc(s.WeightG, ws)
				am := num.Alloc(srcM[i], ms)
				for k, o := range orig {
					basis := in.Mode
					m[o.TripID] = &tripContrib{w: aw[k], m: am[k], basis: basis}
				}
			}
			// Apply this source's loss to its trip breakdown proportionally, and
			// remember which trips the loss came from for origin bookkeeping.
			lossTrips := map[int64]*tripContrib{}
			srcLossTrips[i] = lossTrips
			if wLoss[i] > 0 || mLoss[i] > 0 {
				trips := make([]int64, 0, len(m))
				for t := range m {
					trips = append(trips, t)
				}
				sort.Slice(trips, func(x, y int) bool { return trips[x] < trips[y] })
				wArr := make([]int64, len(trips))
				mArr := make([]int64, len(trips))
				rowSumW, rowSumM := int64(0), int64(0)
				for k, t := range trips {
					wArr[k] = m[t].w
					mArr[k] = m[t].m
					rowSumW += wArr[k]
					rowSumM += mArr[k]
				}
				// trim by loss share of the row so rows sum to retained amounts
				newW := num.Alloc(retW[i], wArr)
				newM := num.Alloc(retM[i], mArr)
				for k, t := range trips {
					lossTrips[t] = &tripContrib{w: wArr[k] - newW[k], m: mArr[k] - newM[k],
						basis: m[t].basis}
					m[t].w = newW[k]
					m[t].m = newM[k]
				}
			}
			srcTrips[i] = m
		}

		// Grade gate for usable destinations.
		destIsUsable := map[int64]bool{}
		for _, pid := range destPiles {
			destIsUsable[pid] = kindByPile[pid] == "usable"
		}
		for _, usable := range []bool{true} {
			_ = usable
		}
		anyUsable := false
		for _, u := range destIsUsable {
			if u {
				anyUsable = true
			}
		}
		if anyUsable {
			if explicit {
				// Only trips that actually route into a usable pile are gated.
				for i, s := range in.Sources {
					if s.DestPileID == nil || !destIsUsable[*s.DestPileID] {
						continue
					}
					trips := make([]int64, 0)
					for t := range srcTrips[i] {
						trips = append(trips, t)
					}
					if err := gradeGuard(ctx, tx, trips, "usable"); err != nil {
						return err
					}
				}
			} else {
				all := make([]int64, 0)
				for _, m := range srcTrips {
					for t := range m {
						all = append(all, t)
					}
				}
				if err := gradeGuard(ctx, tx, all, "usable"); err != nil {
					return err
				}
			}
		}

		// Row (trip) totals across all sources.
		tripWeight := map[int64]int64{}
		tripMetal := map[int64]int64{}
		tripBasis := map[int64]string{}
		for _, m := range srcTrips {
			for t, c := range m {
				tripWeight[t] += c.w
				tripMetal[t] += c.m
				if cur, ok := tripBasis[t]; ok && cur != c.basis {
					tripBasis[t] = "unknown"
				} else if !ok {
					tripBasis[t] = c.basis
				}
			}
		}
		tripIDs := make([]int64, 0, len(tripWeight))
		for t := range tripWeight {
			tripIDs = append(tripIDs, t)
		}
		sort.Slice(tripIDs, func(i, j int) bool { return tripIDs[i] < tripIDs[j] })
		rowW := make([]int64, len(tripIDs))
		for i, t := range tripIDs {
			rowW[i] = tripWeight[t]
		}

		// cell[(pile,trip)] = amount.
		type cell struct {
			w, m  int64
			basis string
			resW  int64
			resM  int64
		}
		cells := map[[2]int64]*cell{}
		if explicit {
			for _, d := range in.Destinations {
				_ = d
			}
			// route each source to its declared pile
			for i, s := range in.Sources {
				pid := *s.DestPileID
				for t, c := range srcTrips[i] {
					k := [2]int64{pid, t}
					cl := cells[k]
					if cl == nil {
						cl = &cell{basis: c.basis}
						cells[k] = cl
					}
					cl.w += c.w
					cl.m += c.m
				}
			}
			// declared destination weights must equal measured sums
			sumByPile := map[int64]int64{}
			for k, c := range cells {
				sumByPile[k[0]] += c.w
			}
			for i, pid := range destPiles {
				if sumByPile[pid] != destWeights[i] {
					return invalid("堆位 %d 转入声明 %d g 与实测来源合计 %d g 不符", pid, destWeights[i], sumByPile[pid])
				}
			}
			// metal per pile is measured; must total retained metal
			var mSum int64
			for _, c := range cells {
				mSum += c.m
			}
			if mSum != retainedM {
				return conflict("实测来源金属量合计 %d mg 与保留金属量 %d mg 不符", mSum, retainedM)
			}
		} else {
			// Material from one trip is homogeneous, so its metal follows its
			// weight split: each destination gets metal × (weight share). Metal is
			// never allocated on its own independent pass, which would let a
			// destination's metal disagree with what its weight implies.
			wMat, wRes := allocateMatrix(rowW, destWeights)
			for i, t := range tripIDs {
				rowM := tripMetal[t]
				mRow := num.Alloc(rowM, wMat[i])
				for j, pid := range destPiles {
					if wMat[i][j] == 0 && mRow[j] == 0 {
						continue
					}
					k := [2]int64{pid, t}
					cl := cells[k]
					if cl == nil {
						cl = &cell{basis: tripBasis[t]}
						cells[k] = cl
					}
					cl.w += wMat[i][j]
					cl.m += mRow[j]
					cl.resW += wRes[i][j]
					// metal remainder vs ideal proportional-to-weight value
					idealM := int64(0)
					if rowW[i] > 0 {
						idealM = num.MulDiv(rowM, wMat[i][j], rowW[i])
					}
					cl.resM += mRow[j] - idealM
				}
			}
			var sW, sM int64
			for _, c := range cells {
				sW += c.w
				sM += c.m
			}
			if sW != inW || sM != retainedM {
				return conflict("分配后重量/金属量与转入不符：%d/%d vs %d/%d", sW, sM, inW, retainedM)
			}
		}

		// Source -> destination weight/metal matrix for links.
		// Uniform: allocate each source's retained amounts across dest weights.
		// Measured: the source routes wholly to its declared pile.
		type linkCell struct{ w, m int64 }
		links := map[[2]int64]*linkCell{} // [srcLot, pile]
		if explicit {
			for i, s := range in.Sources {
				k := [2]int64{s.LotID, *s.DestPileID}
				lc := links[k]
				if lc == nil {
					lc = &linkCell{}
					links[k] = lc
				}
				lc.w += retW[i]
				lc.m += retM[i]
			}
		} else {
			lwMat, _ := allocateMatrix(retW, destWeights)
			for i, s := range in.Sources {
				lmRow := num.Alloc(retM[i], lwMat[i])
				for j, pid := range destPiles {
					if lwMat[i][j] == 0 && lmRow[j] == 0 {
						continue
					}
					links[[2]int64{s.LotID, pid}] = &linkCell{w: lwMat[i][j], m: lmRow[j]}
				}
			}
		}

		// Movement header.
		var occurred any
		if in.OccurredAt != "" && in.OccurredAt != "now" {
			occurred = in.OccurredAt
		}
		if err := tx.QueryRow(ctx, `insert into rt.movements(kind, external_ref, occurred_at, actor_id, note)
			values($1,$2,coalesce($3::timestamptz, now()),$4,$5) returning id`,
			in.Kind, in.ExternalRef, occurred, actor.ID, in.Note).Scan(&movID); err != nil {
			return conflict("转移单 %q 已存在", in.ExternalRef)
		}

		// One new lot per destination pile.
		destLot := map[int64]int64{}
		destWOut := map[int64]int64{}
		destMOut := map[int64]int64{}
		for k, c := range cells {
			destWOut[k[0]] += c.w
			destMOut[k[0]] += c.m
		}
		for _, pid := range destPiles {
			if _, ok := destLot[pid]; ok {
				continue
			}
			known := true
			for k, c := range cells {
				if k[0] == pid && c.basis == "unknown" {
					known = false
				}
			}
			key := fmt.Sprintf("%s-%d-P%d", in.Kind, movID, pid)
			var id int64
			if err := tx.QueryRow(ctx, `insert into rt.inventory_lots(lot_key, pile_id, grade_code, weight_g, metal_mg, metal_known)
				values($1,$2,$3,$4,$5,$6) returning id`,
				key, pid, destGrade[pid], destWOut[pid], destMOut[pid], known).Scan(&id); err != nil {
				return err
			}
			destLot[pid] = id
			if _, err := tx.Exec(ctx, `insert into rt.lot_events(lot_id, movement_id, event, weight_g, metal_mg, pile_id, occurred_at, detail)
				values($1,$2,'in',$3,$4,$5,now(),jsonb_build_object('mode',$6::text,'kind',$7::text))`,
				id, movID, destWOut[pid], destMOut[pid], pid, in.Mode, in.Kind); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `insert into rt.transfer_lines(movement_id, lot_id, direction, weight_g, metal_mg, basis, note)
				values($1,$2,'in',$3,$4,$5,'')`, movID, id, destWOut[pid], destMOut[pid], in.Mode); err != nil {
				return err
			}
		}
		// Origins per destination lot.
		for k, c := range cells {
			pid, t := k[0], k[1]
			if c.w == 0 && c.m == 0 {
				continue
			}
			total := destWOut[pid]
			share := int64(0)
			if total > 0 {
				share = num.MulDiv(c.w, 1_000_000, total)
			}
			if _, err := tx.Exec(ctx, `insert into rt.lot_origins(lot_id, trip_id, weight_g, metal_mg, share_ppm, basis,
				remainder_g, remainder_mg) values($1,$2,$3,$4,$5,$6,$7,$8)`,
				destLot[pid], t, c.w, c.m, share, c.basis, c.resW, c.resM); err != nil {
				return err
			}
		}
		// Links.
		for k, lc := range links {
			if lc.w == 0 {
				continue
			}
			pid := k[1]
			share := int64(0)
			if destWOut[pid] > 0 {
				share = num.MulDiv(lc.w, 1_000_000, destWOut[pid])
			}
			basis := in.Mode
			if _, err := tx.Exec(ctx, `insert into rt.lot_links(movement_id, from_lot_id, to_lot_id, weight_g, metal_mg, share_ppm, basis)
				values($1,$2,$3,$4,$5,$6,$7)`, movID, k[0], destLot[pid], lc.w, lc.m, share, basis); err != nil {
				return err
			}
		}
		// Loss node.
		if lossW > 0 {
			var lossPile int64
			if err := tx.QueryRow(ctx, `select id from rt.piles where kind='loss' order by id limit 1`).Scan(&lossPile); err != nil {
				return blocked("没有损耗堆位，无法登记明确损耗")
			}
			var lossLot int64
			if err := tx.QueryRow(ctx, `insert into rt.inventory_lots(lot_key, pile_id, weight_g, metal_mg, metal_known)
				values($1,$2,$3,$4,true) returning id`,
				fmt.Sprintf("loss-%d", movID), lossPile, lossW, processMetalLoss).Scan(&lossLot); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `insert into rt.lot_events(lot_id, movement_id, event, weight_g, metal_mg, pile_id, occurred_at, detail)
				values($1,$2,'loss',$3,$4,$5,now(),jsonb_build_object('declared_loss_g',$6::bigint,'process_metal_mg',$7::bigint))`,
				lossLot, movID, lossW, processMetalLoss, lossPile, lossW, processMetalLoss); err != nil {
				return err
			}
			for _, l := range in.Losses {
				var m int64
				if l.LossClass == "process" && processLoss > 0 {
					m = num.MulDiv(processMetalLoss, l.WeightG, processLoss)
				}
				if _, err := tx.Exec(ctx, `insert into rt.transfer_lines(movement_id, lot_id, direction, weight_g, metal_mg, basis, loss_class, note)
					values($1,$2,'loss',$3,$4,'measured',$5,$6)`, movID, lossLot, l.WeightG, m, l.LossClass, l.Note); err != nil {
					return err
				}
			}
			// Attribute the loss back to each contributing trip so reconciliation
			// stays per-trip rather than only in total.
			lossByTrip := map[int64][2]int64{}
			for i := range srcLossTrips {
				for t, c := range srcLossTrips[i] {
					cur := lossByTrip[t]
					cur[0] += c.w
					cur[1] += c.m
					lossByTrip[t] = cur
				}
			}
			for t, wm := range lossByTrip {
				if wm[0] == 0 && wm[1] == 0 {
					continue
				}
				share := int64(0)
				if lossW > 0 {
					share = num.MulDiv(wm[0], 1_000_000, lossW)
				}
				if _, err := tx.Exec(ctx, `insert into rt.lot_origins(lot_id, trip_id, weight_g, metal_mg, share_ppm, basis)
					values($1,$2,$3,$4,$5,'measured')`, lossLot, t, wm[0], wm[1], share); err != nil {
					return err
				}
				// Split this trip's loss into drying and process, pro rata to
				// each class's share of the declared loss.
				for _, l := range in.Losses {
					if lossW <= 0 || l.WeightG <= 0 {
						continue
					}
					lw := num.MulDiv(wm[0], l.WeightG, lossW)
					lm := int64(0)
					if l.LossClass == "process" && processMetalLoss > 0 {
						lm = num.MulDiv(wm[1], l.WeightG, lossW)
					}
					if lw == 0 && lm == 0 {
						continue
					}
					if _, err := tx.Exec(ctx, `insert into rt.loss_attribution(movement_id, trip_id, loss_class, weight_g, metal_mg)
						values($1,$2,$3,$4,$5)`, movID, t, l.LossClass, lw, lm); err != nil {
						return err
					}
				}
			}
			for i, s := range in.Sources {
				if wLoss[i] == 0 && mLoss[i] == 0 {
					continue
				}
				share := int64(0)
				if lossW > 0 {
					share = num.MulDiv(wLoss[i], 1_000_000, lossW)
				}
				if _, err := tx.Exec(ctx, `insert into rt.lot_links(movement_id, from_lot_id, to_lot_id, weight_g, metal_mg, share_ppm, basis)
					values($1,$2,$3,$4,$5,$6,'measured')`, movID, s.LotID, lossLot, wLoss[i], mLoss[i], share); err != nil {
					return err
				}
			}
		}
		// Deduct sources. Origins are reduced by exactly what this source sent
		// downstream (retained + loss), so a lot's origin ledger always equals
		// its live balance and a fully consumed lot ends with no origins.
		for i, s := range in.Sources {
			lot := lots[s.LotID]
			if _, err := tx.Exec(ctx, `update rt.inventory_lots set weight_g=weight_g-$2, metal_mg=metal_mg-$3,
				state=case when weight_g-$2 <= 0 then 'consumed' else state end, updated_at=now() where id=$1`,
				lot.ID, s.WeightG, srcM[i]); err != nil {
				return err
			}
			removed := map[int64][2]int64{}
			for t, c := range srcTrips[i] {
				cur := removed[t]
				cur[0] += c.w
				cur[1] += c.m
				removed[t] = cur
			}
			for t, c := range srcLossTrips[i] {
				cur := removed[t]
				cur[0] += c.w
				cur[1] += c.m
				removed[t] = cur
			}
			for t, r := range removed {
				if r[0] == 0 && r[1] == 0 {
					continue
				}
				if _, err := tx.Exec(ctx, `update rt.lot_origins set weight_g=greatest(weight_g-$2,0),
					metal_mg=greatest(metal_mg-$3,0) where lot_id=$1 and trip_id=$4`,
					lot.ID, r[0], r[1], t); err != nil {
					return err
				}
				if _, err := tx.Exec(ctx, `delete from rt.lot_origins where lot_id=$1 and trip_id=$2 and weight_g=0`,
					lot.ID, t); err != nil {
					return err
				}
			}
			if _, err := tx.Exec(ctx, `insert into rt.transfer_lines(movement_id, lot_id, direction, weight_g, metal_mg, basis, note)
				values($1,$2,'out',$3,$4,$5,'')`, movID, lot.ID, s.WeightG, srcM[i], in.Mode); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `insert into rt.lot_events(lot_id, movement_id, event, weight_g, metal_mg, pile_id, occurred_at, detail)
				values($1,$2,'out',$3,$4,$5,now(),'{}'::jsonb)`, lot.ID, movID, s.WeightG, srcM[i], lot.PileID); err != nil {
				return err
			}
		}
		// Structural acyclicity guard: new lots cannot already have outgoing links.
		newIDs := make([]int64, 0, len(destLot))
		for _, id := range destLot {
			newIDs = append(newIDs, id)
		}
		var hasOut bool
		if err := tx.QueryRow(ctx, `select exists(select 1 from rt.lot_links where from_lot_id = any($1::bigint[]))`, newIDs).Scan(&hasOut); err != nil {
			return err
		}
		if hasOut {
			return blocked("新批次已有转出关系，拒绝本次转移")
		}
		_, err = tx.Exec(ctx, `insert into rt.audit_log(actor_id, action, entity, entity_id, detail)
			values($1,'transfer.apply','movement',$2,$3)`, actor.ID, fmt.Sprint(movID), map[string]any{
			"external_ref": in.ExternalRef, "kind": in.Kind, "mode": in.Mode, "out_g": outW,
			"loss_g": lossW, "in_g": inW, "out_metal_mg": outM, "process_metal_loss_mg": processMetalLoss})
		return err
	})
	if err != nil {
		return Movement{}, err
	}
	return a.GetMovementByID(ctx, movID)
}

// TransferPreview validates the transfer shape and shows the per-trip allocation
// that would be produced, without touching any balance.
type TransferPreview struct {
	Valid              bool          `json:"valid"`
	Error              string        `json:"error,omitempty"`
	OutWeightG         int64         `json:"out_weight_g"`
	InWeightG          int64         `json:"in_weight_g"`
	LossG              int64         `json:"loss_g"`
	OutMetalMg         int64         `json:"out_metal_mg"`
	ProcessMetalLossMg int64         `json:"process_metal_loss_mg"`
	PerDestination     []PreviewDest `json:"per_destination"`
	PerTrip            []PreviewTrip `json:"per_trip"`
}

type PreviewDest struct {
	PileID   int64   `json:"pile_id"`
	WeightG  int64   `json:"weight_g"`
	MetalMg  int64   `json:"metal_mg"`
	FromLots []int64 `json:"from_lots"`
}

type PreviewTrip struct {
	TripID  int64 `json:"trip_id"`
	WeightG int64 `json:"weight_g"`
	MetalMg int64 `json:"metal_mg"`
}

func (a *App) PreviewTransfer(ctx context.Context, in TransferInput) (TransferPreview, error) {
	out := TransferPreview{PerDestination: []PreviewDest{}, PerTrip: []PreviewTrip{}}
	if in.ExternalRef == "" {
		out.Error = "转移单号必填"
		return out, nil
	}
	var outW, lossW, inW, outM int64
	var processLoss int64
	ids := []int64{}
	for _, s := range in.Sources {
		outW += s.WeightG
		ids = append(ids, s.LotID)
	}
	for _, l := range in.Losses {
		lossW += l.WeightG
		if l.LossClass == "process" {
			processLoss += l.WeightG
		}
	}
	for _, d := range in.Destinations {
		inW += d.WeightG
	}
	out.OutWeightG, out.InWeightG, out.LossG = outW, inW, lossW
	if inW != outW-lossW {
		out.Error = fmt.Sprintf("转出 %d g - 损耗 %d g ≠ 转入 %d g", outW, lossW, inW)
		return out, nil
	}
	lots, err := a.lotsSnapshot(ctx, ids)
	if err != nil {
		return out, err
	}
	srcM := map[int64]int64{}
	for _, s := range in.Sources {
		l := lots[s.LotID]
		if l == nil {
			out.Error = fmt.Sprintf("批次 %d 不存在", s.LotID)
			return out, nil
		}
		if s.WeightG > l.WeightG {
			out.Error = fmt.Sprintf("批次 %s 余额 %d g 不足以转出 %d g", l.Key, l.WeightG, s.WeightG)
			return out, nil
		}
		if l.State != "available" {
			out.Error = fmt.Sprintf("批次 %s 状态 %s 不可移动", l.Key, l.State)
			return out, nil
		}
		m := l.MetalMg
		if s.WeightG != l.WeightG {
			m = num.MulDiv(l.MetalMg, s.WeightG, l.WeightG)
		}
		srcM[s.LotID] = m
		outM += m
	}
	out.OutMetalMg = outM
	out.ProcessMetalLossMg = num.MulDiv(outM, processLoss, max64(outW, 1))
	destMetal := num.Alloc(out.OutMetalMg-out.ProcessMetalLossMg, destWeightsOf(in))
	for i, d := range in.Destinations {
		out.PerDestination = append(out.PerDestination, PreviewDest{PileID: d.PileID, WeightG: d.WeightG,
			MetalMg: destMetal[i], FromLots: []int64{}})
	}
	// Simple share projection per trip for visibility.
	for _, s := range in.Sources {
		orig, err := a.origins(ctx, s.LotID)
		if err != nil {
			return out, err
		}
		for _, o := range orig {
			if s.WeightG == lots[s.LotID].WeightG {
				out.PerTrip = append(out.PerTrip, PreviewTrip{TripID: o.TripID, WeightG: o.WeightG, MetalMg: o.MetalMg})
			} else if lots[s.LotID].WeightG > 0 {
				out.PerTrip = append(out.PerTrip, PreviewTrip{TripID: o.TripID,
					WeightG: num.MulDiv(o.WeightG, s.WeightG, lots[s.LotID].WeightG),
					MetalMg: num.MulDiv(o.MetalMg, s.WeightG, lots[s.LotID].WeightG)})
			}
		}
	}
	out.Valid = true
	return out, nil
}

func destWeightsOf(in TransferInput) []int64 {
	w := make([]int64, len(in.Destinations))
	for i, d := range in.Destinations {
		w[i] = d.WeightG
	}
	return w
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func (a *App) lotsSnapshot(ctx context.Context, ids []int64) (map[int64]*lotRow, error) {
	rows, err := a.DB.Pool.Query(ctx, `select l.id, l.lot_key, l.pile_id, p.kind, l.weight_g, l.metal_mg, l.state
		from rt.inventory_lots l join rt.piles p on p.id=l.pile_id where l.id = any($1::bigint[])`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]*lotRow{}
	for rows.Next() {
		var l lotRow
		if err := rows.Scan(&l.ID, &l.Key, &l.PileID, &l.PileKind, &l.WeightG, &l.MetalMg, &l.State); err != nil {
			return nil, err
		}
		out[l.ID] = &l
	}
	return out, rows.Err()
}
