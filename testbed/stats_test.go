package shop

import "testing"

func TestMean(t *testing.T) {
	if got := Mean([]int{1, 2}); got != 1.5 {
		t.Errorf("Mean([1 2]) = %v, want 1.5", got)
	}
	if got := Mean(nil); got != 0 {
		t.Errorf("Mean(nil) = %v, want 0", got)
	}
}

func TestMedian(t *testing.T) {
	if got := Median([]float64{3, 1, 2}); got != 2 {
		t.Errorf("odd: got %v, want 2", got)
	}
	if got := Median([]float64{4, 1, 3, 2}); got != 2.5 {
		t.Errorf("even: got %v, want 2.5", got)
	}
}
