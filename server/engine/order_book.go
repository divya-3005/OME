package engine

import "time"

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

// matchBuyOrder matches an incoming Buy order against resting Asks (sells)
func (ob *OrderBook) matchBuyOrder(order *Order) []*Trade {
	trades := make([]*Trade, 0)

	for len(ob.Asks) > 0 && order.Amount > 0 {
		bestAsk := ob.Asks[0]

		// If buyer's price is lower than the cheapest seller, no match
		if order.Price < bestAsk.Price {
			break
		}

		// Match with orders at this price level in FIFO order
		currMaker := bestAsk.Head
		for currMaker != nil && order.Amount > 0 {
			nextMaker := currMaker.Next

			// Calculate how much can trade
			tradeAmount := order.Amount
			if currMaker.Amount < tradeAmount {
				tradeAmount = currMaker.Amount
			}

			// Deduct traded amount
			order.Amount -= tradeAmount
			currMaker.Amount -= tradeAmount
			bestAsk.TotalVolume -= tradeAmount

			trades = append(trades, &Trade{
				MakerOrderID: currMaker.ID,
				TakerOrderID: order.ID,
				Amount:       tradeAmount,
				Price:        currMaker.Price,
				Timestamp:    time.Now().UnixNano(),
			})

			// If maker order is completely filled, remove it
			if currMaker.Amount == 0 {
				bestAsk.RemoveOrder(currMaker)
				delete(ob.Orders, currMaker.ID)
			}

			currMaker = nextMaker
		}

		// If this price level has no more orders, remove it from Asks
		if bestAsk.IsEmpty() {
			ob.Asks = ob.Asks[1:]
		}
	}

	// If the buy order is not completely filled, add remainder to Bids
	if order.Amount > 0 {
		level := ob.getOrCreateBidLevel(order.Price)
		level.AddOrder(order)
		ob.Orders[order.ID] = order
	}

	return trades
}

// matchSellOrder matches an incoming Sell order against resting Bids (buys)
func (ob *OrderBook) matchSellOrder(order *Order) []*Trade {
	trades := make([]*Trade, 0)

	for len(ob.Bids) > 0 && order.Amount > 0 {
		bestBid := ob.Bids[0]

		// If seller's price is higher than the best buyer offers, no match
		if order.Price > bestBid.Price {
			break
		}

		// Match with orders at this price level in FIFO order
		currMaker := bestBid.Head
		for currMaker != nil && order.Amount > 0 {
			nextMaker := currMaker.Next

			tradeAmount := order.Amount
			if currMaker.Amount < tradeAmount {
				tradeAmount = currMaker.Amount
			}

			order.Amount -= tradeAmount
			currMaker.Amount -= tradeAmount
			bestBid.TotalVolume -= tradeAmount

			trades = append(trades, &Trade{
				MakerOrderID: currMaker.ID,
				TakerOrderID: order.ID,
				Amount:       tradeAmount,
				Price:        currMaker.Price,
				Timestamp:    time.Now().UnixNano(),
			})

			if currMaker.Amount == 0 {
				bestBid.RemoveOrder(currMaker)
				delete(ob.Orders, currMaker.ID)
			}

			currMaker = nextMaker
		}

		if bestBid.IsEmpty() {
			ob.Bids = ob.Bids[1:]
		}
	}

	// If the sell order is not completely filled, add remainder to Asks
	if order.Amount > 0 {
		level := ob.getOrCreateAskLevel(order.Price)
		level.AddOrder(order)
		ob.Orders[order.ID] = order
	}

	return trades
}

// ProcessOrder is the main entry point to submit an order
func (ob *OrderBook) ProcessOrder(order *Order) []*Trade {
	if order.Side == Buy {
		return ob.matchBuyOrder(order)
	}
	return ob.matchSellOrder(order)
}

// CancelOrder cancels an existing order by ID
func (ob *OrderBook) CancelOrder(orderID uint64) bool {
	order, exists := ob.Orders[orderID]
	if !exists {
		return false
	}

	if order.Side == Buy {
		for i, level := range ob.Bids {
			if level.Price == order.Price {
				level.RemoveOrder(order)
				if level.IsEmpty() {
					ob.Bids = append(ob.Bids[:i], ob.Bids[i+1:]...)
				}
				break
			}
		}
	} else {
		for i, level := range ob.Asks {
			if level.Price == order.Price {
				level.RemoveOrder(order)
				if level.IsEmpty() {
					ob.Asks = append(ob.Asks[:i], ob.Asks[i+1:]...)
				}
				break
			}
		}
	}

	delete(ob.Orders, orderID)
	return true
}
