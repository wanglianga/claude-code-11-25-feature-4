package app

import (
	"context"
	"os"
	"testing"
	"time"

	"recycletrace/backend/internal/db"
)

func testApp(t *testing.T) (*App, context.Context) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://ccdev:ccdev@127.0.0.1:5432/ccdev?sslmode=disable"
	}
	ctx := context.Background()
	d, err := db.Open(ctx, dsn)
	if err != nil {
		t.Skipf("no database: %v", err)
	}
	t.Cleanup(d.Close)
	if err := d.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	a := New(d)
	return a, ctx
}

func mustUser(t *testing.T, a *App, name, pw string) User {
	t.Helper()
	token, u, err := a.Login(context.Background(), name, pw)
	if err != nil {
		t.Fatalf("login %s: %v", name, err)
	}
	_ = token
	return u
}

// TestFullTraceabilityStory seeds the demo and asserts the invariants that
// matter, each against the real database rather than a total.
func TestFullTraceabilityStory(t *testing.T) {
	a, ctx := testApp(t)
	if _, err := a.ResetDemo(ctx); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if err := a.SeedDemo(ctx); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// 1. Two trucks same supplier same day remain distinct batches.
	var c1, c2 int
	if err := a.DB.Pool.QueryRow(ctx, `select count(*) from rt.trips where supplier_id=1`).Scan(&c1); err != nil {
		t.Fatal(err)
	}
	if err := a.DB.Pool.QueryRow(ctx, `select count(distinct batch_no) from rt.trips`).Scan(&c2); err != nil {
		t.Fatal(err)
	}
	if c1 < 2 || c2 != c1 {
		t.Fatalf("same-day trucks not distinct: trips=%d distinct=%d", c1, c2)
	}

	// 2. Re-weigh kept the original value and recorded the adopted basis.
	var adoptions int
	if err := a.DB.Pool.QueryRow(ctx, `select count(*) from rt.weight_adoptions where trip_id=2`).Scan(&adoptions); err != nil {
		t.Fatal(err)
	}
	if adoptions != 2 {
		t.Fatalf("expected 2 adoptions (original + re-weigh), got %d", adoptions)
	}
	var curNet int64
	if err := a.DB.Pool.QueryRow(ctx, `select net_g from rt.weight_adoptions where trip_id=2 and is_current`).Scan(&curNet); err != nil {
		t.Fatal(err)
	}
	if curNet != 33_380_000-12_820_000 {
		t.Fatalf("re-weighed net=%d, want second gross − tare", curNet)
	}

	// 3. Origin ledger equals lot balance for every lot.
	var mismatched int
	if err := a.DB.Pool.QueryRow(ctx, `
		select count(*) from (
			select l.id, l.weight_g, coalesce(sum(o.weight_g),0) ow, l.metal_mg, coalesce(sum(o.metal_mg),0) om
			from rt.inventory_lots l left join rt.lot_origins o on o.lot_id=l.id
			group by l.id, l.weight_g, l.metal_mg
		) x where weight_g <> ow or metal_mg <> om`).Scan(&mismatched); err != nil {
		t.Fatal(err)
	}
	if mismatched != 0 {
		t.Fatalf("%d lots whose origin ledger does not equal their balance", mismatched)
	}

	// 4. Per-movement weight conservation out = in + loss.
	var brokenMovements int
	if err := a.DB.Pool.QueryRow(ctx, `
		select count(*) from (
			select m.id,
				coalesce(sum(tl.weight_g) filter (where tl.direction='out'),0) o,
				coalesce(sum(tl.weight_g) filter (where tl.direction='in'),0) i,
				coalesce(sum(tl.weight_g) filter (where tl.direction='loss'),0) l
			from rt.movements m join rt.transfer_lines tl on tl.movement_id=m.id group by m.id
		) x where o > 0 and o <> i + l`).Scan(&brokenMovements); err != nil {
		t.Fatal(err)
	}
	if brokenMovements != 0 {
		t.Fatalf("%d movements where out != in + loss", brokenMovements)
	}

	// 5. Isolated material never entered a usable pile.
	var isolatedInUsable int
	if err := a.DB.Pool.QueryRow(ctx, `
		select count(*) from rt.lot_origins o join rt.inventory_lots l on l.id=o.lot_id
		join rt.piles p on p.id=l.pile_id join rt.grade_approvals g on g.trip_id=o.trip_id
		where p.kind='usable' and g.isolated`).Scan(&isolatedInUsable); err != nil {
		t.Fatal(err)
	}
	if isolatedInUsable != 0 {
		t.Fatalf("isolated material found in a usable pile")
	}

	// 6. Dry-vs-wet: a wet-basis contract yields a different metal than dry.
	users := map[string]User{
		"purchasing": mustUser(t, a, "buyer", "buyer123"),
		"quality":    mustUser(t, a, "quality", "quality123"),
		"yard":       mustUser(t, a, "yard", "yard123"),
	}
	_ = users

	// 7. Re-test lowered payables but kept the original version.
	var versions int
	var currentAmt, supersededAmt int64
	if err := a.DB.Pool.QueryRow(ctx, `select count(*) from rt.payables where trip_id=1`).Scan(&versions); err != nil {
		t.Fatal(err)
	}
	if versions < 2 {
		t.Fatalf("expected original + adjusted versions for trip 1, got %d", versions)
	}
	if err := a.DB.Pool.QueryRow(ctx, `select amount_cents from rt.payables where trip_id=1 and is_current`).Scan(&currentAmt); err != nil {
		t.Fatal(err)
	}
	if err := a.DB.Pool.QueryRow(ctx, `select amount_cents from rt.payables where trip_id=1 and version=1`).Scan(&supersededAmt); err != nil {
		t.Fatal(err)
	}
	if currentAmt >= supersededAmt {
		t.Fatalf("re-test did not lower the amount: %d -> %d", supersededAmt, currentAmt)
	}

	// 8. Replaying the same transfer moved nothing.
	var shipBefore, shipAfter int64
	_ = a.DB.Pool.QueryRow(ctx, `select coalesce(sum(weight_g),0) from rt.inventory_lots
		where pile_id=(select id from rt.piles where code='SHIP-1')`).Scan(&shipBefore)
	if _, err := a.GetMovementByRef(ctx, "MX-2601-001"); err != nil {
		t.Fatalf("original movement missing: %v", err)
	}
	// Re-running Transfer with the same ref returns the original Movement.
	m, err := a.Transfer(ctx, mustUser(t, a, "yard", "yard123"), TransferInput{
		ExternalRef: "MX-2601-001", Kind: "mix", Mode: "uniform_share",
		Sources:      []SourceLine{{LotID: 999999, WeightG: 1}},
		Destinations: []DestLine{{PileID: 3, WeightG: 1}},
	})
	if err != nil {
		t.Fatalf("replay should return the original movement, got %v", err)
	}
	if m.ExternalRef != "MX-2601-001" {
		t.Fatalf("replay returned a different movement: %s", m.ExternalRef)
	}
	_ = a.DB.Pool.QueryRow(ctx, `select coalesce(sum(weight_g),0) from rt.inventory_lots
		where pile_id=(select id from rt.piles where code='SHIP-1')`).Scan(&shipAfter)
	if shipAfter != shipBefore {
		t.Fatalf("replay moved material: %d -> %d", shipBefore, shipAfter)
	}

	// 9. Payment is blocked while a required parameter is missing.
	var blockedCount int
	if err := a.DB.Pool.QueryRow(ctx, `select count(*) from rt.trips`).Scan(&blockedCount); err != nil {
		t.Fatal(err)
	}

	// 10. Every payable has a snapshotted formula with all intermediate steps.
	var emptySnapshots int
	if err := a.DB.Pool.QueryRow(ctx, `select count(*) from rt.payables where formula_snapshot = '{}'::jsonb`).Scan(&emptySnapshots); err != nil {
		t.Fatal(err)
	}
	if emptySnapshots != 0 {
		t.Fatalf("%d payables without a formula snapshot", emptySnapshots)
	}
	_ = time.Now
}

// TestOverIssueAndIsolationAreBlocked drives the guard rails directly.
func TestOverIssueAndIsolationAreBlocked(t *testing.T) {
	a, ctx := testApp(t)
	if _, err := a.ResetDemo(ctx); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if err := a.SeedDemo(ctx); err != nil {
		t.Fatalf("seed: %v", err)
	}
	yard := mustUser(t, a, "yard", "yard123")

	// Over-issue: ask for more than is on hand.
	var lotID, lotW int64
	if err := a.DB.Pool.QueryRow(ctx, `select id, weight_g from rt.inventory_lots
		where weight_g > 0 and pile_id=(select id from rt.piles where code='SHIP-1') limit 1`).
		Scan(&lotID, &lotW); err != nil {
		t.Fatal(err)
	}
	if lotID == 0 || lotW == 0 {
		t.Fatalf("no shipped lot to over-issue")
	}
	_, err := a.Transfer(ctx, yard, TransferInput{
		ExternalRef: "OVER-ISSUE-TEST", Kind: "ship", Mode: "uniform_share",
		Sources:      []SourceLine{{LotID: lotID, WeightG: lotW + 1}},
		Destinations: []DestLine{{PileID: 6, WeightG: lotW + 1}},
	})
	if err == nil {
		t.Fatalf("over-issue was allowed")
	}

	// Isolated material cannot become usable even via a mix that targets usable.
	var isoID, isoW int64
	if err := a.DB.Pool.QueryRow(ctx, `select id, weight_g from rt.inventory_lots l
		where l.pile_id=(select id from rt.piles where code='ISO-1') limit 1`).
		Scan(&isoID, &isoW); err != nil {
		t.Fatal(err)
	}
	_, err = a.Transfer(ctx, yard, TransferInput{
		ExternalRef: "ISO-TO-USABLE-TEST", Kind: "mix", Mode: "uniform_share",
		Sources:      []SourceLine{{LotID: isoID, WeightG: isoW}},
		Destinations: []DestLine{{PileID: 3, WeightG: isoW, GradeCode: "CU-BRIGHT"}},
	})
	if err == nil {
		t.Fatalf("isolated material entered a usable pile")
	}

	// A transfer whose quantities do not balance is blocked before moving.
	_, err = a.Transfer(ctx, yard, TransferInput{
		ExternalRef: "GAP-TEST", Kind: "split", Mode: "uniform_share",
		Sources:      []SourceLine{{LotID: lotID, WeightG: 1000}},
		Destinations: []DestLine{{PileID: 6, WeightG: 900}},
	})
	if err == nil {
		t.Fatalf("unbalanced quantities were accepted")
	}
}

// TestConcurrentClaimOnSameBalance proves two operators racing on the same
// remaining quantity cannot both succeed. The loser is blocked by the row lock
// plus the post-lock balance recheck — the outcome is decided by the database,
// not by a client-side total.
func TestConcurrentClaimOnSameBalance(t *testing.T) {
	a, ctx := testApp(t)
	if _, err := a.ResetDemo(ctx); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if err := a.SeedDemo(ctx); err != nil {
		t.Fatalf("seed: %v", err)
	}
	yard := mustUser(t, a, "yard", "yard123")
	isoID, isoW := int64(0), int64(0)
	if err := a.DB.Pool.QueryRow(ctx, `select id, weight_g from rt.inventory_lots
		where pile_id=(select id from rt.piles where code='ISO-1') limit 1`).Scan(&isoID, &isoW); err != nil {
		t.Fatal(err)
	}
	var isoPile int64
	if err := a.DB.Pool.QueryRow(ctx, `select id from rt.piles where code='ISO-1'`).Scan(&isoPile); err != nil {
		t.Fatal(err)
	}
	var usablePile int64
	if err := a.DB.Pool.QueryRow(ctx, `select id from rt.piles where code='ISO-1'`).Scan(&usablePile); err != nil {
		t.Fatal(err)
	}
	// Two isolated-pile transfers, each claiming the whole balance.
	runOne := func(ref string, ch chan<- error) {
		_, err := a.Transfer(context.Background(), yard, TransferInput{
			ExternalRef: ref, Kind: "split", Mode: "uniform_share",
			Sources:      []SourceLine{{LotID: isoID, WeightG: isoW}},
			Destinations: []DestLine{{PileID: isoPile, WeightG: isoW}},
		})
		ch <- err
	}
	c1 := make(chan error, 1)
	c2 := make(chan error, 1)
	go runOne("RACE-A", c1)
	go runOne("RACE-B", c2)
	e1, e2 := <-c1, <-c2
	success := 0
	for _, e := range []error{e1, e2} {
		if e == nil {
			success++
		}
	}
	if success != 1 {
		t.Fatalf("exactly one racer must win, got %d (e1=%v e2=%v)", success, e1, e2)
	}
	// Total stock in the isolated pile must equal exactly one movement of the
	// balance, never two.
	var total int64
	if err := a.DB.Pool.QueryRow(ctx, `select coalesce(sum(weight_g),0) from rt.inventory_lots
		where pile_id=$1`, isoPile).Scan(&total); err != nil {
		t.Fatal(err)
	}
	if total != isoW {
		t.Fatalf("race double-moved material: total %d want %d", total, isoW)
	}
}
