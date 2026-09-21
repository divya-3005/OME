package engine

// Trade represents an executed match between two orders
type Trade struct {
	MakerOrderID uint64 `json:"maker_order_id"`
	TakerOrderID uint64 `json:"taker_order_id"`
	Amount       uint64 `json:"amount"`
	Price        uint64 `json:"price"`
	Timestamp    int64  `json:"timestamp"`
}
