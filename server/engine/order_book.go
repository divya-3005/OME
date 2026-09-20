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

// getOrCreateBidLevel finds an existing Bid level or inserts a new one in descending order
func (ob *OrderBook) getOrCreateBidLevel(price uint64) *PriceLevel {
	for i, level := range ob.Bids {
		if level.Price == price {
			return level
		}
		// If current level price is lower, insert right before it
		if level.Price < price {
			newLevel := NewPriceLevel(price)
			ob.Bids = append(ob.Bids[:i], append([]*PriceLevel{newLevel}, ob.Bids[i:]...)...)
			return newLevel
		}
	}

	// Lowest price so far, append at the end
	newLevel := NewPriceLevel(price)
	ob.Bids = append(ob.Bids, newLevel)
	return newLevel
}

// getOrCreateAskLevel finds an existing Ask level or inserts a new one in ascending order
func (ob *OrderBook) getOrCreateAskLevel(price uint64) *PriceLevel {
	for i, level := range ob.Asks {
		if level.Price == price {
			return level
		}
		// If current level price is higher, insert right before it
		if level.Price > price {
			newLevel := NewPriceLevel(price)
			ob.Asks = append(ob.Asks[:i], append([]*PriceLevel{newLevel}, ob.Asks[i:]...)...)
			return newLevel
		}
	}

	// Highest price so far, append at the end
	newLevel := NewPriceLevel(price)
	ob.Asks = append(ob.Asks, newLevel)
	return newLevel
}


