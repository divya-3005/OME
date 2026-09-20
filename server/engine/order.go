package engine

// Side represents whether an order is buying or selling
type Side int8

const (
	Buy Side = iota
	Sell
)

// Order represents an order placed by a participant
type Order struct {
	ID        uint64 `json:"id"`
	Symbol    string `json:"symbol"`
	Side      Side   `json:"side"`
	Price     uint64 `json:"price"`     // In smallest currency unit (e.g. cents)
	Amount    uint64 `json:"amount"`    // Quantity of shares/units
	Timestamp int64  `json:"timestamp"` // Unix timestamp in nanoseconds for FIFO priority
	Prev *Order `json:"-"`
    Next *Order `json:"-"`
}



