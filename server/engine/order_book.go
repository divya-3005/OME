package engine

import (
	"fmt"
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
	Orders     map[uint64]*Order   // OrderID -> *Order for O(1) cancellations
	seenOrders map[uint64]struct{} // Lifetime order IDs seen by this book
	wal        *WAL                // Optional Write-Ahead Log for crash resilience
}

// NewOrderBook initializes an empty OrderBook for a symbol
func NewOrderBook(symbol string) *OrderBook {
	return &OrderBook{
		Symbol:     symbol,
		Bids:       make([]*PriceLevel, 0),
		Asks:       make([]*PriceLevel, 0),
		Orders:     make(map[uint64]*Order),
		seenOrders: make(map[uint64]struct{}),
	}
}

// SetWAL configures Write-Ahead Logging on this OrderBook.
func (ob *OrderBook) SetWAL(wal *WAL) {
	ob.mu.Lock()
	defer ob.mu.Unlock()
	ob.wal = wal
}

// getOrCreateBidLevel finds an existing Bid level or inserts a new one in descending order using binary search lookup (O(log P)) and slice insertion shift (O(P))
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

// getOrCreateAskLevel finds an existing Ask level or inserts a new one in ascending order using binary search lookup (O(log P)) and slice insertion shift (O(P))
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
			if bestAsk.TotalVolume >= tradeAmount {
				bestAsk.TotalVolume -= tradeAmount
			} else {
				bestAsk.TotalVolume = 0
			}

			trades = append(trades, &Trade{
				Symbol:       ob.Symbol,
				MakerOrderID: currMaker.ID,
				TakerOrderID: order.ID,
				Side:         order.Side, // taker is buying
				Amount:       tradeAmount,
				Price:        currMaker.Price,
				Timestamp:    time.Now().UnixMilli(),
			})

			// If maker order is completely filled, remove it
			if currMaker.Amount == 0 {
				bestAsk.RemoveOrder(currMaker)
				delete(ob.Orders, currMaker.ID)
			}

			currMaker = nextMaker
		}

		// If this price level has no more orders, remove it from Asks without memory leak or capacity loss
		if bestAsk.IsEmpty() {
			copy(ob.Asks, ob.Asks[1:])
			ob.Asks[len(ob.Asks)-1] = nil // Avoid pointer retention in backing array
			ob.Asks = ob.Asks[:len(ob.Asks)-1]
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
			if bestBid.TotalVolume >= tradeAmount {
				bestBid.TotalVolume -= tradeAmount
			} else {
				bestBid.TotalVolume = 0
			}

			trades = append(trades, &Trade{
				Symbol:       ob.Symbol,
				MakerOrderID: currMaker.ID,
				TakerOrderID: order.ID,
				Side:         order.Side,
				Amount:       tradeAmount,
				Price:        currMaker.Price,
				Timestamp:    time.Now().UnixMilli(),
			})

			if currMaker.Amount == 0 {
				bestBid.RemoveOrder(currMaker)
				delete(ob.Orders, currMaker.ID)
			}

			currMaker = nextMaker
		}

		// If this price level has no more orders, remove it from Bids without memory leak or capacity loss
		if bestBid.IsEmpty() {
			copy(ob.Bids, ob.Bids[1:])
			ob.Bids[len(ob.Bids)-1] = nil // Avoid pointer retention in backing array
			ob.Bids = ob.Bids[:len(ob.Bids)-1]
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

// validate checks admission rules. Caller must hold ob.mu.
func (ob *OrderBook) validate(order *Order) error {
	if order == nil {
		return fmt.Errorf("%w: order cannot be nil", ErrInvalidOrder)
	}
	if order.ID == 0 {
		return fmt.Errorf("%w: order ID must be positive", ErrInvalidOrder)
	}
	if order.Symbol != ob.Symbol {
		return fmt.Errorf("%w: symbol %q does not match order book %q", ErrInvalidOrder, order.Symbol, ob.Symbol)
	}
	if _, exists := ob.seenOrders[order.ID]; exists {
		return fmt.Errorf("%w: %d", ErrDuplicateOrderID, order.ID)
	}
	if _, exists := ob.Orders[order.ID]; exists {
		return fmt.Errorf("%w: %d", ErrDuplicateOrderID, order.ID)
	}
	if order.Side != Buy && order.Side != Sell {
		return fmt.Errorf("%w: invalid side %d (must be 0 for Buy or 1 for Sell)", ErrInvalidOrder, order.Side)
	}
	if order.Type != Limit && order.Type != Market {
		return fmt.Errorf("%w: invalid type %d (must be 0 for Limit or 1 for Market)", ErrInvalidOrder, order.Type)
	}
	if order.Amount == 0 || order.Amount > MaxOrderAmount {
		return fmt.Errorf("%w: amount must be between 1 and %d", ErrInvalidOrder, MaxOrderAmount)
	}
	if order.Type == Limit && (order.Price == 0 || order.Price > MaxOrderPrice) {
		return fmt.Errorf("%w: limit price must be between 1 and %d", ErrInvalidOrder, MaxOrderPrice)
	}
	return nil
}

// ProcessOrder validates and matches an order against resting liquidity in Price-Time (FIFO) priority.
// Thread-safe: acquires ob.mu.
func (ob *OrderBook) ProcessOrder(order *Order) ([]*Trade, error) {
	ob.mu.Lock()
	defer ob.mu.Unlock()

	if err := ob.validate(order); err != nil {
		return nil, err
	}

	ob.seenOrders[order.ID] = struct{}{}

	if ob.wal != nil {
		if err := ob.wal.LogPlace(order); err != nil {
			delete(ob.seenOrders, order.ID)
			return nil, err
		}
	}

	if order.Side == Buy {
		return ob.matchBuyOrder(order), nil
	}
	return ob.matchSellOrder(order), nil
}

// CancelOrder removes an active resting order from the book by its ID.
// Runs in O(log P) time to find the price level and O(1) to unlink the order.
func (ob *OrderBook) CancelOrder(orderID uint64) error {
	ob.mu.Lock()
	defer ob.mu.Unlock()

	order, exists := ob.Orders[orderID]
	if !exists {
		return fmt.Errorf("%w: %d", ErrOrderNotFound, orderID)
	}

	if ob.wal != nil {
		if err := ob.wal.LogCancel(ob.Symbol, orderID); err != nil {
			return err
		}
	}

	var level *PriceLevel
	var levelIdx int
	var isBuy bool
	if order.Side == Buy {
		isBuy = true
		levelIdx = sort.Search(len(ob.Bids), func(i int) bool {
			return ob.Bids[i].Price <= order.Price
		})
		if levelIdx < len(ob.Bids) && ob.Bids[levelIdx].Price == order.Price {
			level = ob.Bids[levelIdx]
		}
	} else {
		levelIdx = sort.Search(len(ob.Asks), func(i int) bool {
			return ob.Asks[i].Price >= order.Price
		})
		if levelIdx < len(ob.Asks) && ob.Asks[levelIdx].Price == order.Price {
			level = ob.Asks[levelIdx]
		}
	}

	if level != nil {
		level.RemoveOrder(order)
		if level.IsEmpty() {
			if isBuy {
				copy(ob.Bids[levelIdx:], ob.Bids[levelIdx+1:])
				ob.Bids[len(ob.Bids)-1] = nil
				ob.Bids = ob.Bids[:len(ob.Bids)-1]
			} else {
				copy(ob.Asks[levelIdx:], ob.Asks[levelIdx+1:])
				ob.Asks[len(ob.Asks)-1] = nil
				ob.Asks = ob.Asks[:len(ob.Asks)-1]
			}
		}
	}

	delete(ob.Orders, orderID)
	return nil
}

// TopOfBook returns the best bid and best ask independently.
func (ob *OrderBook) TopOfBook() (bid uint64, hasBid bool, ask uint64, hasAsk bool) {
	ob.mu.RLock()
	defer ob.mu.RUnlock()
	if len(ob.Bids) > 0 {
		bid, hasBid = ob.Bids[0].Price, true
	}
	if len(ob.Asks) > 0 {
		ask, hasAsk = ob.Asks[0].Price, true
	}
	return
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

// OpenOrderSummary provides an immutable summary of a resting order
type OpenOrderSummary struct {
	ID        uint64    `json:"id"`
	Symbol    string    `json:"symbol"`
	Side      Side      `json:"side"`
	Type      OrderType `json:"type"`
	Price     uint64    `json:"price"`
	Amount    uint64    `json:"amount"`
	Timestamp int64     `json:"timestamp"`
}

// GetOpenOrders returns a thread-safe snapshot of all resting orders in the book
func (ob *OrderBook) GetOpenOrders() []OpenOrderSummary {
	ob.mu.RLock()
	defer ob.mu.RUnlock()

	orders := make([]OpenOrderSummary, 0, len(ob.Orders))
	for _, o := range ob.Orders {
		orders = append(orders, OpenOrderSummary{
			ID:        o.ID,
			Symbol:    o.Symbol,
			Side:      o.Side,
			Type:      o.Type,
			Price:     o.Price,
			Amount:    o.Amount,
			Timestamp: o.Timestamp,
		})
	}
	return orders
}
