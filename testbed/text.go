package shop

import "strings"

func WordCount(s string) int {
	return len(strings.Fields(s))
}

func Truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
