package shop

type Cart struct {
	items int
}

type Receipt struct {
	total int
}

// Checkout uses both local types in its signature.
func Checkout(cart Cart) Receipt {
	return Receipt{total: cart.items}
}

// describe uses only primitives, so it links to no local type.
func describe(label string) string {
	return label
}
