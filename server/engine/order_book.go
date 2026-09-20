package engine

// OrderBook manages all bids, asks, and order lookups for a single symbol
type OrderBook struct {
	Symbol string

	// Sorted price levels
	Bids []*PriceLevel // Sorted descending: highest price first
	Asks []*PriceLevel // Sorted ascending: lowest price first

	// Quick lookups
	Orders map[uint64]*Order // OrderID -> *Order for O(1) cancellations
}

// NewOrderBook initializes an empty OrderBook for a symbol
func NewOrderBook(symbol string) *OrderBook {
	return &OrderBook{
		Symbol: symbol,
		Bids:   make([]*PriceLevel, 0),
		Asks:   make([]*PriceLevel, 0),
		Orders: make(map[uint64]*Order),
	}
}
