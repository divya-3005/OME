package engine

import (
	"sync"
	"testing"
	"time"
)

func TestFullMatch(t *testing.T) {
	ob := NewOrderBook("AAPL")

	// Alice wants to sell 10 shares at $100
	aliceSell := &Order{
		ID:        1,
		Symbol:    "AAPL",
		Side:      Sell,
		Price:     100,
		Amount:    10,
		Timestamp: time.Now().UnixNano(),
	}

	// Bob wants to buy 10 shares at $100
	bobBuy := &Order{
		ID:        2,
		Symbol:    "AAPL",
		Side:      Buy,
		Price:     100,
		Amount:    10,
		Timestamp: time.Now().UnixNano(),
	}

	// Alice's order rests on the book (no trades yet)
	trades1, err := ob.ProcessOrder(aliceSell)
	if err != nil {
		t.Fatalf("unexpected error processing aliceSell: %v", err)
	}
	if len(trades1) != 0 {
		t.Fatalf("expected 0 trades, got %d", len(trades1))
	}

	// Bob's order matches with Alice's
	trades2, err := ob.ProcessOrder(bobBuy)
	if err != nil {
		t.Fatalf("unexpected error processing bobBuy: %v", err)
	}
	if len(trades2) != 1 {
		t.Fatalf("expected 1 trade, got %d", len(trades2))
	}

	trade := trades2[0]
	if trade.MakerOrderID != 1 || trade.TakerOrderID != 2 {
		t.Errorf("wrong maker/taker IDs: got maker %d, taker %d", trade.MakerOrderID, trade.TakerOrderID)
	}
	if trade.Amount != 10 || trade.Price != 100 {
		t.Errorf("wrong trade amount or price: got %d @ %d", trade.Amount, trade.Price)
	}

	// Book should now have 0 orders
	if len(ob.Orders) != 0 {
		t.Errorf("expected 0 resting orders, got %d", len(ob.Orders))
	}
}

func TestPartialFillAndFIFO(t *testing.T) {
	ob := NewOrderBook("AAPL")

	// Alice sells 10 @ $100
	if _, err := ob.ProcessOrder(&Order{ID: 1, Symbol: "AAPL", Side: Sell, Price: 100, Amount: 10, Timestamp: 1}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Bob sells 10 @ $100 (arrived later)
	if _, err := ob.ProcessOrder(&Order{ID: 2, Symbol: "AAPL", Side: Sell, Price: 100, Amount: 10, Timestamp: 2}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Charlie buys 15 @ $100
	trades, err := ob.ProcessOrder(&Order{ID: 3, Symbol: "AAPL", Side: Buy, Price: 100, Amount: 15, Timestamp: 3})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(trades) != 2 {
		t.Fatalf("expected 2 trades, got %d", len(trades))
	}

	// Trade 1: Alice (10 shares)
	if trades[0].MakerOrderID != 1 || trades[0].Amount != 10 {
		t.Errorf("expected trade 1 with Alice for 10 shares, got maker %d amount %d", trades[0].MakerOrderID, trades[0].Amount)
	}

	// Trade 2: Bob (5 shares)
	if trades[1].MakerOrderID != 2 || trades[1].Amount != 5 {
		t.Errorf("expected trade 2 with Bob for 5 shares, got maker %d amount %d", trades[1].MakerOrderID, trades[1].Amount)
	}

	// Bob should still have 5 shares left on the book
	remainingOrder, exists := ob.Orders[2]
	if !exists {
		t.Fatalf("expected Bob's order to still exist on the book")
	}
	if remainingOrder.Amount != 5 {
		t.Errorf("expected Bob to have 5 shares remaining, got %d", remainingOrder.Amount)
	}
}

func TestCancelOrder(t *testing.T) {
	ob := NewOrderBook("AAPL")

	order := &Order{
		ID:        1,
		Symbol:    "AAPL",
		Side:      Buy,
		Price:     100,
		Amount:    10,
		Timestamp: time.Now().UnixNano(),
	}

	// Place the order
	if _, err := ob.ProcessOrder(order); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ob.Orders) != 1 {
		t.Fatalf("expected 1 order on book, got %d", len(ob.Orders))
	}

	// Cancel it
	success := ob.CancelOrder(1)
	if !success {
		t.Fatalf("expected cancel to succeed")
	}

	// Verify it's gone
	if len(ob.Orders) != 0 {
		t.Errorf("expected 0 orders in map, got %d", len(ob.Orders))
	}
	if len(ob.Bids) != 0 {
		t.Errorf("expected 0 bid levels, got %d", len(ob.Bids))
	}

	// Cancelling again should return false
	if ob.CancelOrder(1) {
		t.Errorf("expected second cancel to return false")
	}
}

func TestDuplicateOrderID(t *testing.T) {
	ob := NewOrderBook("AAPL")

	order1 := &Order{ID: 42, Symbol: "AAPL", Side: Buy, Type: Limit, Price: 100, Amount: 10}
	_, err := ob.ProcessOrder(order1)
	if err != nil {
		t.Fatalf("unexpected error on first order: %v", err)
	}

	// Attempting to place another order with the same ID must be rejected
	order2 := &Order{ID: 42, Symbol: "AAPL", Side: Buy, Type: Limit, Price: 105, Amount: 5}
	_, err = ob.ProcessOrder(order2)
	if err == nil {
		t.Fatalf("expected error for duplicate order ID, got nil")
	}

	// Original resting order must remain intact and uncorrupted
	resting, exists := ob.Orders[42]
	if !exists || resting.Price != 100 || resting.Amount != 10 {
		t.Fatalf("original order was corrupted or overwritten: %+v", resting)
	}
}

func BenchmarkProcessOrder(b *testing.B) {
	ob := NewOrderBook("AAPL")

	// Pre-populate with 1,000 resting sell orders
	for i := 0; i < 1000; i++ {
		_, _ = ob.ProcessOrder(&Order{
			ID:        uint64(i + 1),
			Symbol:    "AAPL",
			Side:      Sell,
			Price:     uint64(100 + (i % 10)),
			Amount:    10,
			Timestamp: int64(i),
		})
	}

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_, _ = ob.ProcessOrder(&Order{
			ID:        uint64(10000 + i),
			Symbol:    "AAPL",
			Side:      Buy,
			Price:     105,
			Amount:    1,
			Timestamp: int64(i),
		})
	}
}

func TestMarketOrder(t *testing.T) {
	ob := NewOrderBook("AAPL")

	// Alice sells 5 @ $100
	if _, err := ob.ProcessOrder(&Order{ID: 1, Symbol: "AAPL", Side: Sell, Type: Limit, Price: 100, Amount: 5}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Bob sells 10 @ $105
	if _, err := ob.ProcessOrder(&Order{ID: 2, Symbol: "AAPL", Side: Sell, Type: Limit, Price: 105, Amount: 10}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Charlie places a Market Buy for 12 shares
	marketBuy := &Order{
		ID:     3,
		Symbol: "AAPL",
		Side:   Buy,
		Type:   Market,
		Amount: 12,
	}

	trades, err := ob.ProcessOrder(marketBuy)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(trades) != 2 {
		t.Fatalf("expected 2 trades, got %d", len(trades))
	}

	// Trade 1: Alice (5 @ $100)
	if trades[0].MakerOrderID != 1 || trades[0].Amount != 5 || trades[0].Price != 100 {
		t.Errorf("trade 1 mismatch: got maker %d, amount %d, price %d", trades[0].MakerOrderID, trades[0].Amount, trades[0].Price)
	}

	// Trade 2: Bob (7 @ $105)
	if trades[1].MakerOrderID != 2 || trades[1].Amount != 7 || trades[1].Price != 105 {
		t.Errorf("trade 2 mismatch: got maker %d, amount %d, price %d", trades[1].MakerOrderID, trades[1].Amount, trades[1].Price)
	}

	// Bob should have 3 shares remaining on the book
	remainingBob, exists := ob.Orders[2]
	if !exists || remainingBob.Amount != 3 {
		t.Errorf("expected Bob to have 3 shares remaining, got %v", remainingBob)
	}

	// Market order must NOT be on the book
	if _, exists := ob.Orders[3]; exists {
		t.Errorf("market order should not rest on the book")
	}
}

func TestConcurrentOrders(t *testing.T) {
	ob := NewOrderBook("AAPL")
	const numGoroutines = 16
	const ordersPerGoroutine = 200

	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	for g := 0; g < numGoroutines; g++ {
		go func(routineID int) {
			defer wg.Done()
			for i := 0; i < ordersPerGoroutine; i++ {
				orderID := uint64(routineID*100000 + i + 1)
				side := Buy
				if i%2 == 0 {
					side = Sell
				}
				price := uint64(100 + (i % 20))

				// Submit Limit order
				_, _ = ob.ProcessOrder(&Order{
					ID:        orderID,
					Symbol:    "AAPL",
					Side:      side,
					Type:      Limit,
					Price:     price,
					Amount:    5,
					Timestamp: time.Now().UnixNano(),
				})

				// Concurrently read snapshot and top-of-book
				if i%10 == 0 {
					ob.GetSnapshot()
					ob.GetBestBidAsk()
				}

				// Concurrently cancel some orders
				if i%4 == 0 {
					ob.CancelOrder(orderID)
				}
			}
		}(g)
	}

	wg.Wait()
}
