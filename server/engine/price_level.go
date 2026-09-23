package engine

// PriceLevel represents a FIFO queue of orders resting at a specific price point.
//
// Engineering Design Note (Intrusive Doubly Linked List):
// Standard queue implementations (such as Go's container/list or slice queues)
// either incur extra heap allocations per node or require O(N) linear scans
// for arbitrary mid-queue cancellations.
//
// By embedding Prev and Next pointers directly inside the Order struct (an intrusive DLL):
// 1. AddOrder (Enqueue to Tail): O(1) time, 0 extra heap allocations.
// 2. Head Match (Dequeue from Head): O(1) time.
// 3. CancelOrder (Unlink by ID): O(1) time using the Order reference from ob.Orders map.
type PriceLevel struct {
	Price       uint64 // Price in fixed-point cents/ticks
	TotalVolume uint64 // Aggregate shares available at this level
	Head        *Order // Earliest arriving order (FIFO priority)
	Tail        *Order // Most recently placed order
}

// NewPriceLevel creates a new PriceLevel for a given price.
func NewPriceLevel(price uint64) *PriceLevel {
	return &PriceLevel{
		Price: price,
	}
}

// AddOrder appends an order to the back of the queue (Tail) in O(1) time.
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

func (pl *PriceLevel) RemoveOrder(order *Order) {
	if order == nil || pl.Head == nil {
		return
	}
	// Verify order matches this price level and is currently linked in this queue
	if order.Price != pl.Price {
		return
	}
	if order != pl.Head && (order.Prev == nil || order.Prev.Next != order) {
		return
	}
	if order == pl.Head && order.Prev != nil {
		return
	}

	if pl.TotalVolume >= order.Amount {
		pl.TotalVolume -= order.Amount
	} else {
		pl.TotalVolume = 0
	}

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
