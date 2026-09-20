package engine

// PriceLevel represents all orders queued at a specific price
type PriceLevel struct {
	Price       uint64
	TotalVolume uint64
	Head        *Order
	Tail        *Order
}

// NewPriceLevel creates a new PriceLevel for a given price
func NewPriceLevel(price uint64) *PriceLevel {
	return &PriceLevel{
		Price: price,
	}
}

// AddOrder appends an order to the back of the queue (Tail) - O(1)
func (pl *PriceLevel) AddOrder(order *Order) {
	pl.TotalVolume += order.Amount

	// If the line is empty, this order is both Head and Tail
	if pl.Head == nil {
		pl.Head = order
		pl.Tail = order
		order.Prev = nil
		order.Next = nil
		return
	}

	// Otherwise, attach it to the current Tail
	pl.Tail.Next = order
	order.Prev = pl.Tail
	order.Next = nil
	pl.Tail = order
}

// RemoveOrder unlinks an order from anywhere in the queue - O(1)
func (pl *PriceLevel) RemoveOrder(order *Order) {
	pl.TotalVolume -= order.Amount

	// If there is an order in front of it, bridge over
	if order.Prev != nil {
		order.Prev.Next = order.Next
	} else {
		// order was the Head (front of the line)
		pl.Head = order.Next
	}

	// If there is an order behind it, bridge backwards
	if order.Next != nil {
		order.Next.Prev = order.Prev
	} else {
		// order was the Tail (back of the line)
		pl.Tail = order.Prev
	}

	// Disconnect the removed order
	order.Prev = nil
	order.Next = nil
}

// IsEmpty returns true if there are no orders left at this price
func (pl *PriceLevel) IsEmpty() bool {
	return pl.Head == nil
}

