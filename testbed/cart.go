package shop

type Item struct {
	Name     string
	PriceCts int
	Qty      int
}

func Total(items []Item, discountPercent int) int {
	total := 0
	for _, it := range items {
		total += it.PriceCts * it.Qty
	}
	return total + total*discountPercent/100
}
