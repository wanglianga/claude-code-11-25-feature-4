package num

import (
	"errors"
	"fmt"
	"math/big"
)

var ErrMissingParam = errors.New("missing required parameter")
var ErrBasisMismatch = errors.New("formula basis not registered")

// Formula is a registered contract calculation. Each formula fixes the order of
// operations and the base each ratio applies to; ratios are never reused on a
// different base.
type Formula struct {
	Code          string   `json:"code"`
	Label         string   `json:"label"`
	Steps         []string `json:"steps"`
	ImpurityBasis string   `json:"impurity_basis"` // dry | wet
	MetalBasis    string   `json:"metal_basis"`    // dry | wet
	MetalBase     string   `json:"metal_base"`     // priced (dry remaining or wet remaining)
	Note          string   `json:"note"`
}

var Formulas = map[string]Formula{
	"DRY-IMP-METAL": {
		Code:  "DRY-IMP-METAL",
		Label: "湿净重扣水→干重扣单列非金属杂物→剩余干料乘干基金属比例",
		Steps: []string{
			"wet_net = gross - tare (采用称次)",
			"moisture_g = wet_net * moisture_ppm",
			"dry_g = wet_net - moisture_g",
			"impurity_g = dry_g * impurity_ppm (干基，单列扣减)",
			"priced_g = dry_g - impurity_g",
			"metal_g = priced_g * metal_ppm (干基，作用于剩余料)",
			"amount = metal_g / 1e6 t * unit_price * grade_coef",
		},
		ImpurityBasis: "dry",
		MetalBasis:    "dry",
		MetalBase:     "priced_dry",
		Note:          "样例合同。水分只扣一次；杂物与金属比例均以干基定义。",
	},
	"WET-NET-METAL": {
		Code:  "WET-NET-METAL",
		Label: "湿净重扣单列非金属杂物→剩余湿料乘湿基金属比例（独立基准合同）",
		Steps: []string{
			"wet_net = gross - tare (采用称次)",
			"moisture_g = wet_net * moisture_ppm (仅用于干基报表)",
			"dry_g = wet_net - moisture_g",
			"impurity_g = wet_net * impurity_ppm (湿基，单列扣减)",
			"priced_g = wet_net - impurity_g (湿基剩余料)",
			"metal_g = priced_g * metal_ppm (湿基)",
			"amount = metal_g / 1e6 t * unit_price * grade_coef",
		},
		ImpurityBasis: "wet",
		MetalBasis:    "wet",
		MetalBase:     "priced_wet",
		Note:          "另一基准的合同必须登记为本独立公式，不把干基比例套用到湿重上。",
	},
}

// AssayInput holds declared ratios together with the basis they were measured on.
type AssayInput struct {
	MoisturePPM   int64
	ImpurityPPM   *int64 // nil = not provided/not required
	ImpurityBasis string
	MetalPPM      int64
	MetalBasis    string
}

type Inputs struct {
	WetNetG      int64
	Assay        AssayInput
	CoefPPM      int64
	UnitPriceCPT int64
}

// Conversion records any explicit wet<->dry conversion performed, so an audit
// can see that a ratio was not reused on the wrong base.
type Conversion struct {
	Field     string `json:"field"`
	FromBasis string `json:"from_basis"`
	ToBasis   string `json:"to_basis"`
	FromPPM   int64  `json:"from_ppm"`
	ToPPM     int64  `json:"to_ppm"`
}

type Result struct {
	Code         string       `json:"formula_code"`
	WetNetG      int64        `json:"wet_net_g"`
	MoistureG    int64        `json:"moisture_g"`
	DryG         int64        `json:"dry_g"`
	ImpurityG    int64        `json:"impurity_g"`
	PricedG      int64        `json:"priced_g"`
	PricedBasis  string       `json:"priced_basis"`
	MetalG       int64        `json:"metal_g"`
	MetalMg      int64        `json:"metal_mg"`
	AmountCents  int64        `json:"amount_cents"`
	CoefPPM      int64        `json:"coef_ppm"`
	UnitPriceCPT int64        `json:"unit_price_cents_per_t"`
	Conversions  []Conversion `json:"conversions"`
}

// Compute runs the registered formula. It never guesses: a ratio needed by the
// formula but absent from the assay returns ErrMissingParam so payment pauses.
func Compute(formulaCode string, in Inputs) (Result, error) {
	f, ok := Formulas[formulaCode]
	if !ok {
		return Result{}, fmt.Errorf("%w: %q", ErrBasisMismatch, formulaCode)
	}
	if in.WetNetG <= 0 {
		return Result{}, fmt.Errorf("%w: 湿净重", ErrMissingParam)
	}
	if in.Assay.MoisturePPM <= 0 {
		return Result{}, fmt.Errorf("%w: 水分", ErrMissingParam)
	}
	if in.Assay.MoisturePPM >= 1_000_000 {
		return Result{}, fmt.Errorf("%w: 水分比例", ErrMissingParam)
	}
	if in.Assay.MetalPPM <= 0 {
		return Result{}, fmt.Errorf("%w: 金属含量", ErrMissingParam)
	}
	if in.Assay.ImpurityPPM == nil {
		return Result{}, fmt.Errorf("%w: 非金属杂物", ErrMissingParam)
	}
	if *in.Assay.ImpurityPPM < 0 || *in.Assay.ImpurityPPM >= 1_000_000 {
		return Result{}, fmt.Errorf("%w: 非金属杂物比例", ErrMissingParam)
	}
	if in.CoefPPM <= 0 {
		return Result{}, fmt.Errorf("%w: 等级系数", ErrMissingParam)
	}
	if in.UnitPriceCPT <= 0 {
		return Result{}, fmt.Errorf("%w: 合同单价", ErrMissingParam)
	}
	if in.Assay.MetalBasis != "dry" && in.Assay.MetalBasis != "wet" {
		return Result{}, fmt.Errorf("%w: 金属含量基准", ErrMissingParam)
	}
	if in.Assay.ImpurityBasis != "dry" && in.Assay.ImpurityBasis != "wet" {
		return Result{}, fmt.Errorf("%w: 杂物基准", ErrMissingParam)
	}

	res := Result{Code: f.Code, WetNetG: in.WetNetG, CoefPPM: in.CoefPPM, UnitPriceCPT: in.UnitPriceCPT}

	// 1. Moisture is deducted exactly once, always from the wet net weight.
	res.MoistureG = ApplyPPM(in.WetNetG, in.Assay.MoisturePPM)
	res.DryG = in.WetNetG - res.MoistureG
	if res.DryG <= 0 {
		return Result{}, fmt.Errorf("%w: 扣水后干重", ErrMissingParam)
	}

	impPPM := *in.Assay.ImpurityPPM
	impure := in.Assay.ImpurityBasis
	metalPPM := in.Assay.MetalPPM
	metalBasis := in.Assay.MetalBasis

	// `priced` is the material the metal ratio acts on, in the formula's base.
	var pricedBaseMass int64
	switch f.Code {
	case "DRY-IMP-METAL":
		// Impurity must act on dry basis: convert if the assay declared wet.
		if impure == "wet" {
			to := MulDiv(impPPM, in.WetNetG, res.DryG)
			res.Conversions = append(res.Conversions, Conversion{"impurity_ppm", "wet", "dry", impPPM, to})
			impPPM = to
		}
		res.ImpurityG = ApplyPPM(res.DryG, impPPM)
		res.PricedG = res.DryG - res.ImpurityG
		res.PricedBasis = "dry"
		pricedBaseMass = res.PricedG
		if metalBasis != "dry" {
			// metal measured on wet basis: metal_ppm_wet * wet / dry
			to := MulDiv(metalPPM, in.WetNetG, res.DryG)
			res.Conversions = append(res.Conversions, Conversion{"metal_ppm", "wet", "dry", metalPPM, to})
			metalPPM = to
		}
	case "WET-NET-METAL":
		if impure == "dry" {
			to := MulDiv(impPPM, res.DryG, in.WetNetG)
			res.Conversions = append(res.Conversions, Conversion{"impurity_ppm", "dry", "wet", impPPM, to})
			impPPM = to
		}
		res.ImpurityG = ApplyPPM(in.WetNetG, impPPM)
		res.PricedG = in.WetNetG - res.ImpurityG
		res.PricedBasis = "wet"
		pricedBaseMass = res.PricedG
		if metalBasis != "wet" {
			to := MulDiv(metalPPM, res.DryG, in.WetNetG)
			res.Conversions = append(res.Conversions, Conversion{"metal_ppm", "dry", "wet", metalPPM, to})
			metalPPM = to
		}
	default:
		return Result{}, fmt.Errorf("%w: %q", ErrBasisMismatch, f.Code)
	}
	if res.PricedG < 0 {
		return Result{}, fmt.Errorf("%w: 剩余计价材料为负", ErrMissingParam)
	}

	res.MetalG = ApplyPPM(pricedBaseMass, metalPPM)
	res.MetalMg = res.MetalG * 1000

	// amount = metal_g * unit_price_cents_per_t / 1e6 * coef
	// amount_cents = metal_g × unit_price_cents_per_t × coef / 1e12
	//              = metalMg × unit_price × coef / 1e15  (metalMg = metal_g×1000)
	p := new(big.Int).Mul(big.NewInt(res.MetalMg), big.NewInt(in.UnitPriceCPT))
	p.Mul(p, big.NewInt(in.CoefPPM))
	p.Mul(p, big.NewInt(2))
	p.Add(p, big.NewInt(1_000_000_000_000_000))
	p.Quo(p, big.NewInt(2_000_000_000_000_000)) // round half up
	res.AmountCents = p.Int64()

	if res.MetalG <= 0 {
		return Result{}, fmt.Errorf("%w: 计价金属量为零", ErrMissingParam)
	}
	return res, nil
}
