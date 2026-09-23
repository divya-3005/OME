package engine

import (
	"fmt"
	"sync"
	"sync/atomic"
)

// Engine manages multiple OrderBooks across different trading symbols.
// It coordinates symbol routing, sequential order ID generation, and optional WAL persistence.
type Engine struct {
	mu          sync.RWMutex
	orderBooks  map[string]*OrderBook
	nextOrderID uint64
	wal         *WAL
}

// NewEngine initializes an empty exchange engine
func NewEngine() *Engine {
	return &Engine{
		orderBooks: make(map[string]*OrderBook),
	}
}

// SetWAL configures Write-Ahead Logging across all registered symbols and future ones.
func (e *Engine) SetWAL(wal *WAL) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.wal = wal
	for _, ob := range e.orderBooks {
		ob.SetWAL(wal)
	}
}

// NextOrderID generates a monotonically increasing ID. It guarantees
// uniqueness only among IDs it issues itself; it does not prevent a caller
// from supplying an explicit order.ID that collides with a value this
// method later produces, or that is reused across two different symbols.
func (e *Engine) NextOrderID() uint64 {
	return atomic.AddUint64(&e.nextOrderID, 1)
}

// SetMinOrderID ensures the order ID generator never produces an ID <= minID
func (e *Engine) SetMinOrderID(minID uint64) {
	for {
		current := atomic.LoadUint64(&e.nextOrderID)
		if current >= minID {
			break
		}
		if atomic.CompareAndSwapUint64(&e.nextOrderID, current, minID) {
			break
		}
	}
}

// RegisterSymbol creates a new OrderBook for a given symbol
func (e *Engine) RegisterSymbol(symbol string) *OrderBook {
	if symbol == "" {
		return nil
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	if ob, exists := e.orderBooks[symbol]; exists {
		return ob
	}

	ob := NewOrderBook(symbol)
	if e.wal != nil {
		ob.SetWAL(e.wal)
	}
	e.orderBooks[symbol] = ob
	return ob
}

// GetOrderBook retrieves an OrderBook for a given symbol
func (e *Engine) GetOrderBook(symbol string) (*OrderBook, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	ob, exists := e.orderBooks[symbol]
	return ob, exists
}

// ProcessOrder routes an incoming order to its respective OrderBook.
func (e *Engine) ProcessOrder(order *Order) ([]*Trade, error) {
	if order == nil {
		return nil, fmt.Errorf("%w: order cannot be nil", ErrInvalidOrder)
	}
	ob, exists := e.GetOrderBook(order.Symbol)
	if !exists {
		return nil, fmt.Errorf("%w: %q", ErrUnknownSymbol, order.Symbol)
	}
	return ob.ProcessOrder(order)
}

// CancelOrder cancels an order for a given symbol by its ID.
func (e *Engine) CancelOrder(symbol string, orderID uint64) error {
	ob, exists := e.GetOrderBook(symbol)
	if !exists {
		return fmt.Errorf("%w: %q", ErrUnknownSymbol, symbol)
	}
	return ob.CancelOrder(orderID)
}

// GetOpenOrders retrieves all resting orders for a symbol
func (e *Engine) GetOpenOrders(symbol string) ([]OpenOrderSummary, bool) {
	ob, exists := e.GetOrderBook(symbol)
	if !exists {
		return nil, false
	}
	return ob.GetOpenOrders(), true
}
