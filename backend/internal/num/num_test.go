package num

import (
	"math/rand"
	"testing"
)

func TestAllocSumsExactly(t *testing.T) {
	cases := [][]int64{
		{100}, {1, 1, 1}, {5, 3, 2}, {7, 0, 0}, {123, 456, 789, 1011},
	}
	for _, w := range cases {
		var sum int64
		for _, x := range w {
			sum += x
		}
		for total := int64(0); total <= sum; total++ {
			got := Alloc(total, w)
			var s int64
			for i, v := range got {
				if v < 0 || v > w[i] {
					t.Fatalf("out of range: total=%d w=%v got=%v", total, w, got)
				}
				s += v
			}
			if s != total {
				t.Fatalf("sum mismatch total=%d w=%v got=%v", total, w, got)
			}
		}
	}
}

func TestAllocRandomConservation(t *testing.T) {
	r := rand.New(rand.NewSource(7))
	for iter := 0; iter < 2000; iter++ {
		n := r.Intn(8) + 1
		w := make([]int64, n)
		var sum int64
		for i := range w {
			w[i] = r.Int63n(1_000_000)
			sum += w[i]
		}
		total := r.Int63n(sum + 1)
		got := Alloc(total, w)
		var s int64
		for i := range got {
			if got[i] > w[i] || got[i] < 0 {
				t.Fatalf("range violated")
			}
			s += got[i]
		}
		if s != total {
			t.Fatalf("conservation violated: total=%d got=%v", total, got)
		}
	}
}

func TestMulDivRound(t *testing.T) {
	if got := MulDivRound(1, 1, 2); got != 1 {
		t.Fatalf("want 1 got %d", got)
	}
	if got := MulDivRound(1, 1, 3); got != 0 {
		t.Fatalf("want 0 got %d", got)
	}
	if got := MulDivRound(1_000_000_000, 1, 3); got != 333333333 {
		t.Fatalf("want 333333333 got %d", got)
	}
}
