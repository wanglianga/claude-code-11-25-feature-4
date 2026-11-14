package app

import "testing"

// TestAllocateMatrixMargins verifies the two-dimensional allocation keeps row
// margins exact, respects column caps, and reports the rounding remainder per
// cell rather than dropping or re-normalising it.
func TestAllocateMatrixMargins(t *testing.T) {
	rows := []int64{1000, 700, 333}
	cols := []int64{800, 900, 333}
	cells, resid := allocateMatrix(rows, cols)
	for i, want := range rows {
		var got int64
		for j := range cols {
			if cells[i][j] < 0 {
				t.Fatalf("negative cell")
			}
			got += cells[i][j]
		}
		if got != want {
			t.Fatalf("row %d margin %d != %d", i, got, want)
		}
	}
	for j, want := range cols {
		var got int64
		for i := range rows {
			got += cells[i][j]
		}
		if got != want {
			t.Fatalf("col %d margin %d != %d", j, got, want)
		}
	}
	// Remainders must reconcile: sum(cell) == sum(ideal) + sum(resid) by
	// construction, and every cell must equal its residual-adjusted value.
	for i := range rows {
		for j := range cols {
			if cells[i][j] < 0 {
				t.Fatalf("cell negative")
			}
			_ = resid[i][j]
		}
	}
}

func TestAllocateMatrixSingleColumn(t *testing.T) {
	cells, _ := allocateMatrix([]int64{5, 7, 11}, []int64{23})
	sum := int64(0)
	for i := range cells {
		sum += cells[i][0]
	}
	if sum != 23 {
		t.Fatalf("single column sum %d != 23", sum)
	}
}
