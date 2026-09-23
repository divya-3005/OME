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

// ProcessOrderWithWALNotify routes an order to its book. See OrderBook.ProcessOrderWithWALNotify.
func (e *Engine) ProcessOrderWithWALNotify(order *Order, wal *WAL, notify func(trades []*Trade, rested bool)) ([]*Trade, error) {
	if order == nil {
		return nil, fmt.Errorf("%w: order cannot be nil", ErrInvalidOrder)
	}
	ob, exists := e.GetOrderBook(order.Symbol)
	if !exists {
		return nil, fmt.Errorf("%w: %q", ErrUnknownSymbol, order.Symbol)
	}
	return ob.ProcessOrderWithWALNotify(order, wal, notify)
}

// ProcessOrderWithWAL routes an order to its book with WAL persistence.
func (e *Engine) ProcessOrderWithWAL(order *Order, wal *WAL) ([]*Trade, error) {
	return e.ProcessOrderWithWALNotify(order, wal, nil)
}

// ProcessOrder routes an incoming order to its respective OrderBook without WAL
func (e *Engine) ProcessOrder(order *Order) ([]*Trade, error) {
	return e.ProcessOrderWithWAL(order, nil)
}

// CancelOrderWithWALNotify cancels an order in the given symbol's book.
func (e *Engine) CancelOrderWithWALNotify(symbol string, orderID uint64, wal *WAL, notify func()) (bool, error) {
	ob, exists := e.GetOrderBook(symbol)
	if !exists {
		return false, fmt.Errorf("%w: %q", ErrUnknownSymbol, symbol)
	}
	return ob.CancelOrderWithWALNotify(orderID, wal, notify)
}

// CancelOrderWithWAL cancels an order with WAL persistence.
func (e *Engine) CancelOrderWithWAL(symbol string, orderID uint64, wal *WAL) (bool, error) {
	return e.CancelOrderWithWALNotify(symbol, orderID, wal, nil)
}

// CancelOrder cancels an order for a given symbol without WAL
func (e *Engine) CancelOrder(symbol string, orderID uint64) (bool, error) {
	return e.CancelOrderWithWAL(symbol, orderID, nil)
}
