// Package num provides exact integer arithmetic for mass, metal and money.
// Everything is integers: grams, milligrams of metal, cents.
package num

import (
	"math/big"
	"sort"
)

// Alloc distributes total across weights using the largest-remainder method.
// The returned slice always sums exactly to total. When total <= sum(weights)
// every element is <= its weight; callers guarantee that for capacity-bounded
// allocations. Total is not clamped, so the same helper can distribute a
// different unit (e.g. metal mg) in proportion to weight shares.
// Ties are broken by larger weight, then lower index.
func Alloc(total int64, weights []int64) []int64 {
	out := make([]int64, len(weights))
	if len(weights) == 0 || total <= 0 {
		return out
	}
	var sum int64
	for _, w := range weights {
		if w > 0 {
			sum += w
		}
	}
	if sum <= 0 {
		return out
	}
	type rem struct {
		idx  int
		frac int64
		w    int64
	}
	rems := make([]rem, 0, len(weights))
	var assigned int64
	for i, w := range weights {
		if w <= 0 {
			continue
		}
		base := mulDiv(total, w, sum)
		out[i] = base
		assigned += base
		rems = append(rems, rem{idx: i, frac: total*w - base*sum, w: w})
	}
	left := total - assigned
	sort.SliceStable(rems, func(a, b int) bool {
		if rems[a].frac != rems[b].frac {
			return rems[a].frac > rems[b].frac
		}
		if rems[a].w != rems[b].w {
			return rems[a].w > rems[b].w
		}
		return rems[a].idx < rems[b].idx
	})
	for i := int64(0); i < left; i++ {
		r := rems[int(i)%len(rems)]
		out[r.idx]++
	}
	return out
}

// mulDiv computes floor(a*b/c) without overflow.
func mulDiv(a, b, c int64) int64 {
	if c == 0 {
		return 0
	}
	p := new(big.Int).Mul(big.NewInt(a), big.NewInt(b))
	return new(big.Int).Quo(p, big.NewInt(c)).Int64()
}

// MulDiv is the exported floor(a*b/c).
func MulDiv(a, b, c int64) int64 { return mulDiv(a, b, c) }

// MulDivRound is round-half-up(a*b/c) for non-negative inputs.
func MulDivRound(a, b, c int64) int64 {
	if c == 0 {
		return 0
	}
	p := new(big.Int).Mul(big.NewInt(a), big.NewInt(b))
	p.Mul(p, big.NewInt(2))
	p.Add(p, big.NewInt(c))
	p.Quo(p, big.NewInt(2*c))
	return p.Int64()
}

// ApplyPPM returns value * ppm / 1e6, floor.
func ApplyPPM(v, ppm int64) int64 { return mulDiv(v, ppm, 1_000_000) }

// RemainderPPM is value - ApplyPPM(value, ppm).
func RemainderPPM(v, ppm int64) int64 { return v - ApplyPPM(v, ppm) }

// KgToG converts kilograms with up to 6 decimals (as integer micrograms) to grams.
func KgToG(micrograms int64) int64 { return micrograms / 1000 }
