package engine

import (
	"fmt"
	"sync"
	"sync/atomic"
)

// Engine manages multiple OrderBooks across different trading symbols
type Engine struct {
	mu          sync.RWMutex
	orderBooks  map[string]*OrderBook
	nextOrderID uint64
}

// NewEngine initializes an empty exchange engine
func NewEngine() *Engine {
	return &Engine{
		orderBooks: make(map[string]*OrderBook),
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
	e.mu.Lock()
	defer e.mu.Unlock()

	if ob, exists := e.orderBooks[symbol]; exists {
		return ob
	}

	ob := NewOrderBook(symbol)
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

// ProcessOrderWithWAL routes an incoming order to its respective OrderBook with atomic WAL logging & fsync
func (e *Engine) ProcessOrderWithWAL(order *Order, wal *WAL) ([]*Trade, error) {
	if order == nil {
		return nil, fmt.Errorf("order cannot be nil")
	}

	ob, exists := e.GetOrderBook(order.Symbol)
	if !exists {
		return nil, fmt.Errorf("symbol %s not supported", order.Symbol)
	}

	return ob.ProcessOrderWithWAL(order, wal)
}

// ProcessOrder routes an incoming order to its respective OrderBook without WAL
func (e *Engine) ProcessOrder(order *Order) ([]*Trade, error) {
	return e.ProcessOrderWithWAL(order, nil)
}

// CancelOrderWithWAL cancels an order for a given symbol with atomic WAL logging & fsync
func (e *Engine) CancelOrderWithWAL(symbol string, orderID uint64, wal *WAL) (bool, error) {
	ob, exists := e.GetOrderBook(symbol)
	if !exists {
		return false, fmt.Errorf("symbol %s not supported", symbol)
	}

	return ob.CancelOrderWithWAL(orderID, wal)
}

// CancelOrder cancels an order for a given symbol without WAL
func (e *Engine) CancelOrder(symbol string, orderID uint64) (bool, error) {
	return e.CancelOrderWithWAL(symbol, orderID, nil)
}
