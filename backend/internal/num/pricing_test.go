package num

import "testing"

func ptr(v int64) *int64 { return &v }

func TestSampleContractDryFirst(t *testing.T) {
	// 28,580 kg wet net, 3.200% moisture, 1.100% impurity on dry, 62.500% metal on dry.
	res, err := Compute("DRY-IMP-METAL", Inputs{
		WetNetG: 28_580_000,
		Assay: AssayInput{MoisturePPM: 32_000, ImpurityPPM: ptr(11_000), ImpurityBasis: "dry",
			MetalPPM: 625_000, MetalBasis: "dry"},
		CoefPPM: 1_000_000, UnitPriceCPT: 5_800_000,
	})
	if err != nil {
		t.Fatalf("compute: %v", err)
	}
	wantMoisture := int64(914_560) // 28,580,000 × 32,000 ppm
	if res.MoistureG != wantMoisture {
		t.Fatalf("moisture=%d want %d", res.MoistureG, wantMoisture)
	}
	if res.DryG != 28_580_000-wantMoisture {
		t.Fatalf("dry=%d", res.DryG)
	}
	wantImp := ApplyPPM(res.DryG, 11_000)
	if res.ImpurityG != wantImp {
		t.Fatalf("impurity=%d want %d", res.ImpurityG, wantImp)
	}
	if res.PricedG != res.DryG-res.ImpurityG {
		t.Fatalf("priced=%d", res.PricedG)
	}
	wantMetal := ApplyPPM(res.PricedG, 625_000)
	if res.MetalG != wantMetal {
		t.Fatalf("metal=%d want %d", res.MetalG, wantMetal)
	}
	// manual amount: metal_g × 5,800,000 × 1.0 / 1e6 = metal_g × 5.8
	wantAmount := res.MetalG * 5_800_000 / 1_000_000
	if res.AmountCents != wantAmount {
		t.Fatalf("amount=%d want %d", res.AmountCents, wantAmount)
	}
	if res.MoistureG+res.DryG != res.WetNetG {
		t.Fatalf("wet = moisture+dry identity broken")
	}
}

func TestWetBasisContractIsSeparate(t *testing.T) {
	// Same ratios on the wet basis must NOT reuse the dry-basis result.
	dry, _ := Compute("DRY-IMP-METAL", Inputs{WetNetG: 10_000_000,
		Assay: AssayInput{MoisturePPM: 50_000, ImpurityPPM: ptr(20_000), ImpurityBasis: "dry",
			MetalPPM: 600_000, MetalBasis: "dry"}, CoefPPM: 1_000_000, UnitPriceCPT: 5_000_000})
	wet, _ := Compute("WET-NET-METAL", Inputs{WetNetG: 10_000_000,
		Assay: AssayInput{MoisturePPM: 50_000, ImpurityPPM: ptr(20_000), ImpurityBasis: "wet",
			MetalPPM: 600_000, MetalBasis: "wet"}, CoefPPM: 1_000_000, UnitPriceCPT: 5_000_000})
	if dry.MetalG == wet.MetalG {
		t.Fatalf("dry and wet basis produced identical metal, ratios reused across bases")
	}
	if wet.PricedG != 10_000_000-ApplyPPM(10_000_000, 20_000) {
		t.Fatalf("wet priced=%d", wet.PricedG)
	}
}

func TestMissingParameterBlocks(t *testing.T) {
	_, err := Compute("DRY-IMP-METAL", Inputs{WetNetG: 1_000_000,
		Assay:   AssayInput{MoisturePPM: 30_000, MetalPPM: 600_000, MetalBasis: "dry"},
		CoefPPM: 1_000_000, UnitPriceCPT: 1_000_000})
	if err == nil {
		t.Fatal("expected block when impurity is missing")
	}
	if err != ErrMissingParam && !isMissing(err) {
		t.Fatalf("unexpected error type: %v", err)
	}
}

func isMissing(err error) bool {
	for err != nil {
		if err == ErrMissingParam {
			return true
		}
		err = unwrap(err)
	}
	return false
}

func unwrap(err error) error {
	type wrapper interface{ Unwrap() error }
	if w, ok := err.(wrapper); ok {
		return w.Unwrap()
	}
	return nil
}

func TestConversionIsRecorded(t *testing.T) {
	// impurity declared on wet basis for a dry contract must be converted and
	// the conversion must be visible.
	res, err := Compute("DRY-IMP-METAL", Inputs{WetNetG: 10_000_000,
		Assay: AssayInput{MoisturePPM: 100_000, ImpurityPPM: ptr(18_000), ImpurityBasis: "wet",
			MetalPPM: 600_000, MetalBasis: "dry"}, CoefPPM: 1_000_000, UnitPriceCPT: 5_000_000})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(res.Conversions) != 1 {
		t.Fatalf("expected 1 conversion, got %d", len(res.Conversions))
	}
	if res.Conversions[0].Field != "impurity_ppm" || res.Conversions[0].FromBasis != "wet" {
		t.Fatalf("unexpected conversion: %+v", res.Conversions[0])
	}
}
