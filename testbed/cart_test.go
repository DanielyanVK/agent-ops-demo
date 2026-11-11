package shop

import "testing"

func TestTotal(t *testing.T) {
	items := []Item{{"pen", 150, 2}, {"book", 1000, 1}}
	if got := Total(items, 0); got != 1300 {
		t.Errorf("no discount: got %d, want 1300", got)
	}
	if got := Total(items, 10); got != 1170 {
		t.Errorf("10%% discount: got %d, want 1170", got)
	}
}
