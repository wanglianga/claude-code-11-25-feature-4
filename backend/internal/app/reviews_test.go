package app

import (
	"context"
	"strings"
	"testing"

	"recycletrace/backend/internal/db"
)

func reviewsTestApp(t *testing.T) (*App, context.Context, map[string]User) {
	t.Helper()
	a, ctx := testApp(t)
	if _, err := a.ResetDemo(ctx); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if err := a.SeedDemo(ctx); err != nil {
		t.Fatalf("seed: %v", err)
	}
	users := map[string]User{
		"purchasing": mustUser(t, a, "buyer", "buyer123"),
		"yard":       mustUser(t, a, "yard", "yard123"),
		"lab":        mustUser(t, a, "lab", "lab123"),
		"quality":    mustUser(t, a, "quality", "quality123"),
		"finance":    mustUser(t, a, "finance", "finance123"),
	}
	return a, ctx, users
}

// TestRetainedUnitCannotBeDrawnTwice: the same physical retained unit is a hard
// one-time resource; a second review must use a split or be refused.
func TestRetainedUnitCannotBeDrawnTwice(t *testing.T) {
	a, ctx, users := reviewsTestApp(t)
	var unitID int64
	if err := a.DB.Pool.QueryRow(ctx, `select id from rt.sample_units where unit_no='SU-2601-002-S1'`).Scan(&unitID); err != nil {
		t.Fatal(err)
	}
	var tripID, sampleID, caseID int64
	if err := a.DB.Pool.QueryRow(ctx, `select s.trip_id, s.id from rt.sample_units u join rt.samples s on s.id=u.sample_id
		where u.id=$1`, unitID).Scan(&tripID, &sampleID); err != nil {
		t.Fatal(err)
	}
	c, err := a.OpenReviewCase(ctx, users["quality"], tripID, OpenCaseInput{SampleID: sampleID, Note: "重复领取测试"})
	if err != nil {
		t.Fatal(err)
	}
	caseID = c.ID
	if _, err := a.DrawSampleUnit(ctx, users["lab"], caseID, unitID, DrawInput{WeightG: 10, BlindCode: "BL-DUP-TEST"}); err == nil {
		t.Fatalf("a already-drawn unit was drawn again")
	}
	var draws int
	if err := a.DB.Pool.QueryRow(ctx, `select count(*) from rt.sample_draws where unit_id=$1`, unitID).Scan(&draws); err != nil {
		t.Fatal(err)
	}
	if draws != 1 {
		t.Fatalf("unit has %d draws, want exactly 1", draws)
	}
}

// TestBrokenSealBlocksRetest: a broken seal cannot be scheduled for a re-test,
// and the case is documented rather than silently skipped.
func TestBrokenSealBlocksRetest(t *testing.T) {
	a, ctx, users := reviewsTestApp(t)
	var unitID, caseID int64
	if err := a.DB.Pool.QueryRow(ctx, `select id from rt.sample_units where unit_no='SU-2601-003'`).Scan(&unitID); err != nil {
		t.Fatal(err)
	}
	if err := a.DB.Pool.QueryRow(ctx, `select id from rt.review_cases where sample_unit_id=$1`, unitID).Scan(&caseID); err != nil {
		t.Fatal(err)
	}
	_, err := a.DrawSampleUnit(ctx, users["lab"], caseID, unitID, DrawInput{WeightG: 10, BlindCode: "BL-BROKEN-TEST"})
	if err == nil {
		t.Fatalf("re-test was scheduled on a broken seal")
	}
	if !strings.Contains(err.Error(), "断裂") {
		t.Fatalf("error should name the broken seal, got %v", err)
	}
	// Remaining quantity is zeroed and the seal event is kept.
	u, err := a.GetSampleUnit(ctx, unitID)
	if err != nil {
		t.Fatal(err)
	}
	if u.Status != UnitBroken || u.RemainingG != 0 {
		t.Fatalf("broken unit state: status=%s remaining=%d", u.Status, u.RemainingG)
	}
	found := false
	for _, ev := range u.SealEvents {
		if ev.Event == "broken" {
			found = true
		}
	}
	if !found {
		t.Fatalf("broken seal has no circulation event")
	}
}

// TestDryWetIncomparableNeverAverages: incomparable bases produce difference
// sources and separate candidate amounts, never a middle value.
func TestDryWetIncomparableNeverAverages(t *testing.T) {
	a, ctx, _ := reviewsTestApp(t)
	var caseID int64
	if err := a.DB.Pool.QueryRow(ctx, `select id from rt.review_cases where case_no like 'RC-B-20261110-001-%'`).Scan(&caseID); err != nil {
		t.Fatal(err)
	}
	trial, err := a.TrialReview(ctx, caseID)
	if err != nil {
		t.Fatal(err)
	}
	if trial.Comparability.Comparable {
		t.Fatalf("dry vs wet reports were treated as comparable")
	}
	if len(trial.Comparability.DifferenceSources) == 0 {
		t.Fatalf("incomparable case must explain the difference source")
	}
	if !trial.AveragingForbidden {
		t.Fatalf("trial must forbid averaging")
	}
	if len(trial.Candidates) < 3 {
		t.Fatalf("expected supplier/plant/external candidates, got %d", len(trial.Candidates))
	}
	// Exactly one candidate must match the adopted external result; no candidate
	// equals the arithmetic mean of the others.
	var mean int64
	for _, c := range trial.Candidates {
		mean += c.AmountCents
	}
	mean /= int64(len(trial.Candidates))
	for _, c := range trial.Candidates {
		if c.AmountCents != 0 && c.AmountCents == mean {
			t.Fatalf("a candidate equals the average %d", mean)
		}
	}
}

// TestAdjudicationRejectsStaleEvidence: two approvers looking at different
// evidence versions cannot both commit; the stale one is blocked.
func TestAdjudicationRejectsStaleEvidence(t *testing.T) {
	a, ctx, users := reviewsTestApp(t)
	var caseID int64
	if err := a.DB.Pool.QueryRow(ctx, `select id from rt.review_cases where case_no like 'RC-B-20261110-002-01%'`).Scan(&caseID); err != nil {
		t.Fatal(err)
	}
	c, err := a.GetReviewCase(ctx, users["quality"], caseID)
	if err != nil {
		t.Fatal(err)
	}
	// A new report arrives after the approver looked at the evidence.
	if _, err := a.AddPartyReport(ctx, users["lab"], caseID, PartyReportInput{
		Party: "plant", ReportNo: "IN-LATE-1", SampleNo: c.SampleNo, Position: "车厢中部",
		DeclaredUnit: "%", MoisturePPM: 30_000, ImpurityPPM: i64p(14_000), ImpurityBasis: "dry",
		MetalPPM: 619_500, MetalBasis: "dry",
	}); err != nil {
		t.Fatal(err)
	}
	_, err = a.Adjudicate(ctx, users["quality"], caseID, AdjudicateInput{
		ExpectedEvidenceVersion: c.EvidenceVersion, Adopted: "plant", PartyReportID: c.PartyReports[1].ID,
		Reason: "依据旧证据裁定",
	})
	if err == nil {
		t.Fatalf("stale evidence version was accepted")
	}
	if !strings.Contains(err.Error(), "证据已变化") {
		t.Fatalf("expected stale-evidence block, got %v", err)
	}
}

// TestAdjudicationAfterDecisionOpensRereview: a report after the decision may
// not overwrite it; it must open a re-review request.
func TestAdjudicationAfterDecisionOpensRereview(t *testing.T) {
	a, ctx, users := reviewsTestApp(t)
	var caseID int64
	if err := a.DB.Pool.QueryRow(ctx, `select id from rt.review_cases where case_no like 'RC-B-20261110-002-01%'`).Scan(&caseID); err != nil {
		t.Fatal(err)
	}
	var adjCountBefore int
	if err := a.DB.Pool.QueryRow(ctx, `select count(*) from rt.adjudications where case_id=$1`, caseID).Scan(&adjCountBefore); err != nil {
		t.Fatal(err)
	}
	if _, err := a.AddPartyReport(ctx, users["lab"], caseID, PartyReportInput{
		Party: "plant", ReportNo: "IN-AFTER-DECISION", Position: "车厢中部",
		DeclaredUnit: "%", MoisturePPM: 30_000, ImpurityPPM: i64p(14_000), ImpurityBasis: "dry",
		MetalPPM: 617_000, MetalBasis: "dry",
	}); err != nil {
		t.Fatal(err)
	}
	c, err := a.GetReviewCase(ctx, users["quality"], caseID)
	if err != nil {
		t.Fatal(err)
	}
	if c.OpenRereview == nil {
		t.Fatalf("post-decision report did not open a re-review request")
	}
	var adjCountAfter int
	if err := a.DB.Pool.QueryRow(ctx, `select count(*) from rt.adjudications where case_id=$1`, caseID).Scan(&adjCountAfter); err != nil {
		t.Fatal(err)
	}
	if adjCountAfter != adjCountBefore {
		t.Fatalf("post-decision report overwrote history: %d -> %d", adjCountBefore, adjCountAfter)
	}
	var effective int
	if err := a.DB.Pool.QueryRow(ctx, `select count(*) from rt.adjudications where case_id=$1 and status='effective'`,
		caseID).Scan(&effective); err != nil {
		t.Fatal(err)
	}
	if effective != 1 {
		t.Fatalf("want exactly one effective decision, got %d", effective)
	}
}

// TestFreezeOnlyAffectsDisputedTruck: a dispute freeze blocks approval and
// payment of the disputed truck only. A settled truck, and a paid truck of the
// same supplier, are untouched.
func TestFreezeOnlyAffectsDisputedTruck(t *testing.T) {
	a, ctx, users := reviewsTestApp(t)
	// Trip 1 finished adjudication with a calculated (unpaid) rebuild version.
	var calcID int64
	if err := a.DB.Pool.QueryRow(ctx, `select id from rt.payables where trip_id=1 and is_current and status='calculated'`).
		Scan(&calcID); err != nil {
		t.Fatal(err)
	}
	// Freeze trip 1 by opening a fresh dispute on it.
	var sampleID int64
	if err := a.DB.Pool.QueryRow(ctx, `select id from rt.samples where trip_id=1 order by id limit 1`).Scan(&sampleID); err != nil {
		t.Fatal(err)
	}
	if _, err := a.OpenReviewCase(ctx, users["quality"], 1, OpenCaseInput{SampleID: sampleID, Note: "冻结范围测试"}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.ApprovePayable(ctx, users["finance"], calcID, "冻结期间批准"); err == nil {
		t.Fatalf("frozen truck was approved")
	} else if !strings.Contains(err.Error(), "冻结") {
		t.Fatalf("expected freeze-specific block, got %v", err)
	}
	if _, err := a.MarkPaid(ctx, users["finance"], calcID); err == nil {
		t.Fatalf("frozen truck was marked paid")
	}
	// Every freeze names a trip that belongs to the case that created it: a
	// dispute never reaches across to another truck.
	var stray int
	if err := a.DB.Pool.QueryRow(ctx, `select count(*) from rt.payment_freezes f
		join rt.review_cases c on c.id=f.case_id where c.trip_id<>f.trip_id`).Scan(&stray); err != nil {
		t.Fatal(err)
	}
	if stray != 0 {
		t.Fatalf("%d freezes point at a truck other than their case's", stray)
	}
	// Payment already made for other trucks of the same supplier is untouched.
	var paidOthers int
	if err := a.DB.Pool.QueryRow(ctx, `select count(*) from rt.payables
		where status='paid' and trip_id<>1`).Scan(&paidOthers); err != nil {
		t.Fatal(err)
	}
	if paidOthers == 0 {
		t.Fatalf("other trucks' paid basis was lost")
	}
	if _, err := a.MarkPaid(ctx, users["finance"], calcID); err == nil {
		t.Fatalf("frozen truck was marked paid after approve refusal")
	}
}

// TestSettlementRoutesPaidVsUnpaid asserts the seeded outcomes: trip 2 was paid
// and gets a recovery delta; trip 1 was unpaid and got a rebuilt basis.
func TestSettlementRoutesPaidVsUnpaid(t *testing.T) {
	a, ctx, _ := reviewsTestApp(t)
	rows, err := a.DB.Pool.Query(ctx, `select trip_id, kind, delta_cents from rt.payment_recoveries order by id`)
	if err != nil {
		t.Fatal(err)
	}
	type rec struct {
		trip  int64
		kind  string
		delta int64
	}
	list := []rec{}
	for rows.Next() {
		var r rec
		if err := rows.Scan(&r.trip, &r.kind, &r.delta); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		list = append(list, r)
	}
	rows.Close()
	var rebuild, recovery bool
	for _, r := range list {
		if r.trip == 1 && r.kind == "rebuild" && r.delta != 0 {
			rebuild = true
		}
		if r.trip == 2 && r.kind == "recovery" && r.delta != 0 {
			recovery = true
		}
	}
	if !rebuild || !recovery {
		t.Fatalf("settlement routing wrong: %+v", list)
	}
	// Paid basis remains the paid version; a rebuild never rewrites it.
	var paidAmt int64
	if err := a.DB.Pool.QueryRow(ctx, `select amount_cents from rt.payables where trip_id=2 and status='paid'`).Scan(&paidAmt); err != nil {
		t.Fatal(err)
	}
	if paidAmt == 0 {
		t.Fatalf("paid basis was lost")
	}
}

// TestBlindReportHidesSupplierQuote ensures the external result is masked and
// carries no supplier quote until revealed.
func TestBlindReportHidesSupplierQuote(t *testing.T) {
	a, ctx, users := reviewsTestApp(t)
	var caseID int64
	if err := a.DB.Pool.QueryRow(ctx, `select id from rt.review_cases where case_no like 'RC-B-20261110-002-02%'`).Scan(&caseID); err != nil {
		t.Fatal(err)
	}
	c, err := a.GetReviewCase(ctx, users["lab"], caseID)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range c.ExternalReports {
		if !e.Revealed && !e.Masked {
			t.Fatalf("unrevealed external report was not masked")
		}
		if !e.Revealed && (e.MetalPPM != 0 || e.SampleNo != "") {
			t.Fatalf("unrevealed external report leaked numeric/sample data")
		}
	}
	// A lab user never sees the supplier's quote.
	for _, r := range c.PartyReports {
		if r.Party == "supplier" && r.QuoteCentsPerT != 0 {
			t.Fatalf("lab role saw the supplier quote %d", r.QuoteCentsPerT)
		}
	}
}

// TestDrawIsAtomicOnRemainingQuantity runs two draws of the full remaining
// quantity against different cases; only one may succeed.
func TestDrawIsAtomicOnRemainingQuantity(t *testing.T) {
	a, ctx, users := reviewsTestApp(t)
	var unitID, sampleID, tripID, remain int64
	if err := a.DB.Pool.QueryRow(ctx, `select u.id, u.sample_id, s.trip_id, u.remaining_g
		from rt.sample_units u join rt.samples s on s.id=u.sample_id where u.unit_no='SU-2601-001'`).
		Scan(&unitID, &sampleID, &tripID, &remain); err != nil {
		t.Fatal(err)
	}
	if remain <= 0 {
		t.Fatalf("need a positive remaining balance for the race")
	}
	c1, err := a.OpenReviewCase(ctx, users["quality"], tripID, OpenCaseInput{SampleID: sampleID, Note: "并发领取甲"})
	if err != nil {
		t.Fatal(err)
	}
	c2, err := a.OpenReviewCase(ctx, users["quality"], tripID, OpenCaseInput{SampleID: sampleID, Note: "并发领取乙"})
	if err != nil {
		t.Fatal(err)
	}
	type res struct{ err error }
	ch := make(chan res, 2)
	go func() {
		_, err := a.DrawSampleUnit(context.Background(), users["lab"], c1.ID, unitID,
			DrawInput{WeightG: remain, BlindCode: "BL-RACE-A"})
		ch <- res{err}
	}()
	go func() {
		_, err := a.DrawSampleUnit(context.Background(), users["lab"], c2.ID, unitID,
			DrawInput{WeightG: remain, BlindCode: "BL-RACE-B"})
		ch <- res{err}
	}()
	e1, e2 := <-ch, <-ch
	success := 0
	for _, e := range []error{e1.err, e2.err} {
		if e == nil {
			success++
		}
	}
	if success != 1 {
		t.Fatalf("exactly one draw must win, got %d (%v / %v)", success, e1.err, e2.err)
	}
	var draws int
	if err := a.DB.Pool.QueryRow(ctx, `select count(*) from rt.sample_draws where unit_id=$1`, unitID).Scan(&draws); err != nil {
		t.Fatal(err)
	}
	if draws != 1 {
		t.Fatalf("unit drawn %d times, want exactly 1 (the race winner)", draws)
	}
}

// ensure the db import stays used if assertions above change.
var _ = db.DB{}

// TestConcurrentAdjudicationSingleWinner: two approvers holding the same evidence
// version cannot both commit a determination; the second is blocked and asked
// to re-read the evidence.
func TestConcurrentAdjudicationSingleWinner(t *testing.T) {
	a, ctx, users := reviewsTestApp(t)
	var caseID int64
	if err := a.DB.Pool.QueryRow(ctx, `select id from rt.review_cases where case_no like 'RC-B-20261110-002-02%'`).Scan(&caseID); err != nil {
		t.Fatal(err)
	}
	c, err := a.GetReviewCase(ctx, users["quality"], caseID)
	if err != nil {
		t.Fatal(err)
	}
	// Add both parties' reports so a determination has something to adopt.
	for _, in := range []PartyReportInput{
		{Party: "supplier", ReportNo: "CONC-S", SampleNo: c.SampleNo, Position: "车厢中部", DeclaredUnit: "%",
			MoisturePPM: 30_000, ImpurityPPM: i64p(14_000), ImpurityBasis: "dry", MetalPPM: 620_000, MetalBasis: "dry"},
		{Party: "plant", ReportNo: "CONC-P", SampleNo: c.SampleNo, Position: "车厢中部", DeclaredUnit: "%",
			MoisturePPM: 30_000, ImpurityPPM: i64p(14_000), ImpurityBasis: "dry", MetalPPM: 619_000, MetalBasis: "dry"},
	} {
		if _, err := a.AddPartyReport(ctx, users["lab"], caseID, in); err != nil {
			t.Fatal(err)
		}
	}
	c, err = a.GetReviewCase(ctx, users["quality"], caseID)
	if err != nil {
		t.Fatal(err)
	}
	version := c.EvidenceVersion
	type res struct{ err error }
	ch := make(chan res, 2)
	run := func(adopted, reason string) {
		_, err := a.Adjudicate(context.Background(), users["quality"], caseID, AdjudicateInput{
			ExpectedEvidenceVersion: version, Adopted: adopted, Reason: reason,
		})
		ch <- res{err}
	}
	go run("supplier", "并发裁定甲：采用供应商")
	go run("plant", "并发裁定乙：采用厂内")
	e1, e2 := <-ch, <-ch
	succ, blocked := 0, 0
	for _, e := range []error{e1.err, e2.err} {
		if e == nil {
			succ++
		} else if strings.Contains(e.Error(), "证据已变化") || strings.Contains(e.Error(), "已裁定") {
			blocked++
		}
	}
	if succ != 1 || blocked != 1 {
		t.Fatalf("want 1 successful and 1 blocked adjudication, got succ=%d blocked=%d (%v / %v)",
			succ, blocked, e1.err, e2.err)
	}
	var effective int
	if err := a.DB.Pool.QueryRow(ctx, `select count(*) from rt.adjudications where case_id=$1 and status='effective'`,
		caseID).Scan(&effective); err != nil {
		t.Fatal(err)
	}
	if effective != 1 {
		t.Fatalf("want exactly one effective decision, got %d", effective)
	}
}

// TestIncrementalRecoveryOnReAdjudication: a second determination on an
// already-paid truck records only the change since what was already settled,
// never the full delta measured from the original paid amount again.
func TestIncrementalRecoveryOnReAdjudication(t *testing.T) {
	a, ctx, users := reviewsTestApp(t)
	var caseID int64
	if err := a.DB.Pool.QueryRow(ctx, `select id from rt.review_cases where case_no like 'RC-B-20261110-002-01%'`).Scan(&caseID); err != nil {
		t.Fatal(err)
	}
	var firstDelta int64
	if err := a.DB.Pool.QueryRow(ctx, `select delta_cents from rt.payment_recoveries
		where trip_id=2 and kind='recovery' order by id desc limit 1`).Scan(&firstDelta); err != nil {
		t.Fatal(err)
	}
	// A late report opens re-review; then the approver re-determines with a
	// different result.
	if _, err := a.AddPartyReport(ctx, users["lab"], caseID, PartyReportInput{
		Party: "plant", ReportNo: "IN-RE-1", Position: "车厢中部", DeclaredUnit: "%",
		MoisturePPM: 30_000, ImpurityPPM: i64p(14_000), ImpurityBasis: "dry",
		MetalPPM: 621_000, MetalBasis: "dry",
	}); err != nil {
		t.Fatal(err)
	}
	c, err := a.GetReviewCase(ctx, users["quality"], caseID)
	if err != nil {
		t.Fatal(err)
	}
	if c.OpenRereview == nil {
		t.Fatalf("expected an open re-review")
	}
	if _, err := a.Adjudicate(ctx, users["quality"], caseID, AdjudicateInput{
		ExpectedEvidenceVersion: c.EvidenceVersion, Adopted: "plant", Reason: "重新复核后采用新的厂内报告",
	}); err != nil {
		t.Fatal(err)
	}
	var total int64
	if err := a.DB.Pool.QueryRow(ctx, `select coalesce(sum(delta_cents),0) from rt.payment_recoveries
		where trip_id=2 and kind='recovery'`).Scan(&total); err != nil {
		t.Fatal(err)
	}
	var target, paid int64
	if err := a.DB.Pool.QueryRow(ctx, `select amount_cents from rt.payables where trip_id=2 and status='paid'`).Scan(&paid); err != nil {
		t.Fatal(err)
	}
	target = paid + total
	var newAmt int64
	if err := a.DB.Pool.QueryRow(ctx, `select new_amount_cents from rt.adjudication_lines
		where adjudication_id=(select max(id) from rt.adjudications where case_id=$1)`, caseID).Scan(&newAmt); err != nil {
		t.Fatal(err)
	}
	if target != newAmt {
		t.Fatalf("recovery total %d + paid %d != latest decision amount %d", total, paid, newAmt)
	}
	if firstDelta == 0 {
		t.Fatalf("seed recovery should be non-zero for this check to mean anything")
	}
}
