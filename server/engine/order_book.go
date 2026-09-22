package engine

import (
	"sort"
	"sync"
	"time"
)

// PriceLevelSummary provides an immutable snapshot of volume at a price level
type PriceLevelSummary struct {
	Price  uint64 `json:"price"`
	Volume uint64 `json:"volume"`
}

// OrderBook manages all bids, asks, and order lookups for a single symbol.
// It is protected by an internal RWMutex to ensure thread-safe concurrent access.
type OrderBook struct {
	mu     sync.RWMutex
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

// getOrCreateBidLevel finds an existing Bid level or inserts a new one in descending order using binary search (O(log P))
func (ob *OrderBook) getOrCreateBidLevel(price uint64) *PriceLevel {
	// Bids are sorted descending: level.Price <= price
	idx := sort.Search(len(ob.Bids), func(i int) bool {
		return ob.Bids[i].Price <= price
	})

	if idx < len(ob.Bids) && ob.Bids[idx].Price == price {
		return ob.Bids[idx]
	}

	newLevel := NewPriceLevel(price)
	ob.Bids = append(ob.Bids, nil)
	copy(ob.Bids[idx+1:], ob.Bids[idx:])
	ob.Bids[idx] = newLevel
	return newLevel
}

// getOrCreateAskLevel finds an existing Ask level or inserts a new one in ascending order using binary search (O(log P))
func (ob *OrderBook) getOrCreateAskLevel(price uint64) *PriceLevel {
	// Asks are sorted ascending: level.Price >= price
	idx := sort.Search(len(ob.Asks), func(i int) bool {
		return ob.Asks[i].Price >= price
	})

	if idx < len(ob.Asks) && ob.Asks[idx].Price == price {
		return ob.Asks[idx]
	}

	newLevel := NewPriceLevel(price)
	ob.Asks = append(ob.Asks, nil)
	copy(ob.Asks[idx+1:], ob.Asks[idx:])
	ob.Asks[idx] = newLevel
	return newLevel
}

// matchBuyOrder matches an incoming Buy order against resting Asks (sells)
func (ob *OrderBook) matchBuyOrder(order *Order) []*Trade {
	trades := make([]*Trade, 0)

	for len(ob.Asks) > 0 && order.Amount > 0 {
		bestAsk := ob.Asks[0]

		// If buyer's price is lower than the cheapest seller, no match
		if order.Type == Limit && order.Price < bestAsk.Price {
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
	if order.Type == Limit && order.Amount > 0 {
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
		if order.Type == Limit && order.Price > bestBid.Price {
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
	if order.Type == Limit && order.Amount > 0 {
		level := ob.getOrCreateAskLevel(order.Price)
		level.AddOrder(order)
		ob.Orders[order.ID] = order
	}

	return trades
}

// ProcessOrder is the thread-safe entry point to submit an order
func (ob *OrderBook) ProcessOrder(order *Order) []*Trade {
	ob.mu.Lock()
	defer ob.mu.Unlock()

	if order.Side == Buy {
		return ob.matchBuyOrder(order)
	}
	return ob.matchSellOrder(order)
}

// CancelOrder is the thread-safe entry point to cancel an existing order by ID
func (ob *OrderBook) CancelOrder(orderID uint64) bool {
	ob.mu.Lock()
	defer ob.mu.Unlock()

	order, exists := ob.Orders[orderID]
	if !exists {
		return false
	}

	if order.Side == Buy {
		idx := sort.Search(len(ob.Bids), func(i int) bool {
			return ob.Bids[i].Price <= order.Price
		})
		if idx < len(ob.Bids) && ob.Bids[idx].Price == order.Price {
			level := ob.Bids[idx]
			level.RemoveOrder(order)
			if level.IsEmpty() {
				ob.Bids = append(ob.Bids[:idx], ob.Bids[idx+1:]...)
			}
		}
	} else {
		idx := sort.Search(len(ob.Asks), func(i int) bool {
			return ob.Asks[i].Price >= order.Price
		})
		if idx < len(ob.Asks) && ob.Asks[idx].Price == order.Price {
			level := ob.Asks[idx]
			level.RemoveOrder(order)
			if level.IsEmpty() {
				ob.Asks = append(ob.Asks[:idx], ob.Asks[idx+1:]...)
			}
		}
	}

	delete(ob.Orders, orderID)
	return true
}

// GetSnapshot returns a thread-safe copy of bids and asks for L2 market data
func (ob *OrderBook) GetSnapshot() ([]PriceLevelSummary, []PriceLevelSummary) {
	ob.mu.RLock()
	defer ob.mu.RUnlock()

	bids := make([]PriceLevelSummary, len(ob.Bids))
	for i, b := range ob.Bids {
		bids[i] = PriceLevelSummary{Price: b.Price, Volume: b.TotalVolume}
	}

	asks := make([]PriceLevelSummary, len(ob.Asks))
	for i, a := range ob.Asks {
		asks[i] = PriceLevelSummary{Price: a.Price, Volume: a.TotalVolume}
	}

	return bids, asks
}

// GetBestBidAsk returns the top-of-book best bid and best ask prices thread-safely
func (ob *OrderBook) GetBestBidAsk() (bestBid uint64, bestAsk uint64, ok bool) {
	ob.mu.RLock()
	defer ob.mu.RUnlock()

	if len(ob.Bids) == 0 || len(ob.Asks) == 0 {
		return 0, 0, false
	}

	return ob.Bids[0].Price, ob.Asks[0].Price, true
}
