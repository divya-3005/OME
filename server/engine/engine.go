package engine

import (
	"fmt"
	"sync"
)

// Engine manages multiple OrderBooks across different trading symbols
type Engine struct {
	mu         sync.RWMutex
	orderBooks map[string]*OrderBook
}

// NewEngine initializes an empty exchange engine
func NewEngine() *Engine {
	return &Engine{
		orderBooks: make(map[string]*OrderBook),
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

// ProcessOrder routes an incoming order to its respective OrderBook
func (e *Engine) ProcessOrder(order *Order) ([]*Trade, error) {
	ob, exists := e.GetOrderBook(order.Symbol)
	if !exists {
		return nil, fmt.Errorf("symbol %s not supported", order.Symbol)
	}

	return ob.ProcessOrder(order), nil
}

// CancelOrder cancels an order for a given symbol
func (e *Engine) CancelOrder(symbol string, orderID uint64) (bool, error) {
	ob, exists := e.GetOrderBook(symbol)
	if !exists {
		return false, fmt.Errorf("symbol %s not supported", symbol)
	}

	return ob.CancelOrder(orderID), nil
}

