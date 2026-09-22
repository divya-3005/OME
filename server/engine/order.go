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

// Order represents an order placed by a participant
type Order struct {
	ID        uint64    `json:"id"`
	Symbol    string    `json:"symbol"`
	Side      Side      `json:"side"`
	Type      OrderType `json:"type"`      // Limit or Market
	Price     uint64    `json:"price"`     // In smallest currency unit (ignored for Market orders)
	Amount    uint64    `json:"amount"`    // Quantity of shares/units
	Timestamp int64     `json:"timestamp"` // Unix timestamp in nanoseconds for FIFO priority
	Prev      *Order    `json:"-"`
	Next      *Order    `json:"-"`
}
