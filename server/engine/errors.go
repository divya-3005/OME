package engine

import "errors"

// Sentinel errors so callers can map failures to the right HTTP status.
var (
	ErrInvalidOrder     = errors.New("invalid order")
	ErrDuplicateOrderID = errors.New("duplicate order ID")
	ErrUnknownSymbol    = errors.New("symbol not supported")
	ErrOrderNotFound    = errors.New("order not found")
	ErrWAL              = errors.New("write-ahead log failure")
)

// Upper bounds on order fields. They prevent uint64 overflow in PriceLevel.TotalVolume
// and in filled/remaining calculations, and keep every value below 2^53 so browser
// clients (JavaScript numbers) can represent them exactly.
const (
	MaxOrderAmount uint64 = 1_000_000_000
	MaxOrderPrice  uint64 = 1_000_000_000_000
)
