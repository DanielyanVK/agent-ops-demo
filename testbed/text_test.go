package shop

import "testing"

func TestWordCount(t *testing.T) {
	if got := WordCount("  one two\tthree\n"); got != 3 {
		t.Errorf("got %d, want 3", got)
	}
}

func TestTruncate(t *testing.T) {
	cases := []struct {
		in   string
		n    int
		want string
	}{
		{"hello", 5, "hello"},
		{"hello world", 5, "hell…"},
		{"привет мир", 6, "приве…"},
	}
	for _, c := range cases {
		if got := Truncate(c.in, c.n); got != c.want {
			t.Errorf("Truncate(%q, %d) = %q, want %q", c.in, c.n, got, c.want)
		}
	}
}
