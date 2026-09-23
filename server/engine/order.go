package engine

// Side represents whether an order is buying or selling
type Side int8

const (
	Buy Side = iota
	Sell
)

// OrderType represents whether an order is a Limit order or Market order
type OrderType int8

const (
	Limit OrderType = iota
	Market
)

// Order represents an order placed by a participant in the market.
//
// Engineering Note:
// All prices and amounts use fixed-point uint64 (cents/ticks) to guarantee
// exact arithmetic without IEEE-754 floating-point rounding errors.
// Prev and Next pointers form an intrusive doubly linked list inside PriceLevel.
type Order struct {
	ID        uint64    `json:"id"`
	Symbol    string    `json:"symbol"`
	Side      Side      `json:"side"`      // 0 = Buy, 1 = Sell
	Type      OrderType `json:"type"`      // 0 = Limit, 1 = Market
	Price     uint64    `json:"price"`     // In smallest currency unit/cents (e.g., $150.00 = 15000)
	Amount    uint64    `json:"amount"`    // Quantity of shares/units
	Timestamp int64     `json:"timestamp"` // Server-assigned arrival time in unix milliseconds
	Prev      *Order    `json:"-"`         // Intrusive linked list pointer (previous order at this price)
	Next      *Order    `json:"-"`         // Intrusive linked list pointer (next order at this price)
}
