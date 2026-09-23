package engine

// Trade represents an executed match between two orders
type Trade struct {
	Symbol       string `json:"symbol"`
	MakerOrderID uint64 `json:"maker_order_id"`
	TakerOrderID uint64 `json:"taker_order_id"`
	Side         Side   `json:"side"` // Side of the taker (aggressor) order: 0=Buy, 1=Sell
	Amount       uint64 `json:"amount"`
	Price        uint64 `json:"price"`
	Timestamp    int64  `json:"timestamp"`
}
