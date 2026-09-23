package engine

import (
	"errors"
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
		Timestamp: time.Now().UnixMilli(),
	}

	// Bob wants to buy 10 shares at $100
	bobBuy := &Order{
		ID:        2,
		Symbol:    "AAPL",
		Side:      Buy,
		Price:     100,
		Amount:    10,
		Timestamp: time.Now().UnixMilli(),
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
		Timestamp: time.Now().UnixMilli(),
	}

	// Place the order
	if _, err := ob.ProcessOrder(order); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ob.Orders) != 1 {
		t.Fatalf("expected 1 order on book, got %d", len(ob.Orders))
	}

	// Cancel it
	if err := ob.CancelOrder(1); err != nil {
		t.Fatalf("expected cancel to succeed, got %v", err)
	}

	// Verify it's gone
	if len(ob.Orders) != 0 {
		t.Errorf("expected 0 orders in map, got %d", len(ob.Orders))
	}
	if len(ob.Bids) != 0 {
		t.Errorf("expected 0 bid levels, got %d", len(ob.Bids))
	}

	// Cancelling again should return ErrOrderNotFound
	if err := ob.CancelOrder(1); !errors.Is(err, ErrOrderNotFound) {
		t.Errorf("expected second cancel to return ErrOrderNotFound, got %v", err)
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

	nextID := uint64(1)
	for i := 0; i < 1000; i++ {
		_, _ = ob.ProcessOrder(&Order{
			ID:        nextID,
			Symbol:    "AAPL",
			Side:      Sell,
			Price:     uint64(100 + (i % 10)),
			Amount:    10,
			Timestamp: int64(i),
		})
		nextID++
	}

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		trades, err := ob.ProcessOrder(&Order{
			ID: nextID, Symbol: "AAPL", Side: Buy, Type: Limit,
			Price: 105, Amount: 1, Timestamp: int64(i),
		})
		nextID++
		if err != nil || len(trades) != 1 {
			b.Fatalf("iteration %d: buy must match exactly one resting ask (err=%v, trades=%d)", i, err, len(trades))
		}

		// Put the consumed unit back at the exact price it traded, so the book shape never
		// changes and every iteration exercises the matching path.
		if _, err := ob.ProcessOrder(&Order{
			ID: nextID, Symbol: "AAPL", Side: Sell, Type: Limit,
			Price: trades[0].Price, Amount: 1, Timestamp: int64(i),
		}); err != nil {
			b.Fatal(err)
		}
		nextID++
	}
}

func BenchmarkProcessOrderWithWAL(b *testing.B) {
	dir := b.TempDir()
	wal, err := OpenWAL(dir + "/bench_wal.log")
	if err != nil {
		b.Fatalf("failed to open WAL: %v", err)
	}
	defer wal.Close()

	ob := NewOrderBook("AAPL")
	ob.SetWAL(wal)
	nextID := uint64(1)
	for i := 0; i < 1000; i++ {
		_, _ = ob.ProcessOrder(&Order{
			ID: nextID, Symbol: "AAPL", Side: Sell,
			Price: uint64(100 + (i % 10)), Amount: 10, Timestamp: int64(i),
		})
		nextID++
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		trades, err := ob.ProcessOrder(&Order{
			ID: nextID, Symbol: "AAPL", Side: Buy, Type: Limit,
			Price: 105, Amount: 1, Timestamp: int64(i),
		})
		nextID++
		if err != nil || len(trades) != 1 {
			b.Fatalf("iteration %d: buy must match exactly one resting ask (err=%v, trades=%d)", i, err, len(trades))
		}

		if _, err := ob.ProcessOrder(&Order{
			ID: nextID, Symbol: "AAPL", Side: Sell, Type: Limit,
			Price: trades[0].Price, Amount: 1, Timestamp: int64(i),
		}); err != nil {
			b.Fatal(err)
		}
		nextID++
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
					Timestamp: time.Now().UnixMilli(),
				})

				// Concurrently read snapshot and top-of-book
				if i%10 == 0 {
					ob.GetSnapshot()
					ob.TopOfBook()
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

func TestRejectsOversizedAmount(t *testing.T) {
	ob := NewOrderBook("AAPL")
	_, err := ob.ProcessOrder(&Order{ID: 1, Symbol: "AAPL", Side: Buy, Type: Limit, Price: 100, Amount: MaxOrderAmount + 1})
	if !errors.Is(err, ErrInvalidOrder) {
		t.Fatalf("expected ErrInvalidOrder, got %v", err)
	}
}

func TestRemoveOrderUnlinkedSafe(t *testing.T) {
	pl := NewPriceLevel(100)
	order1 := &Order{ID: 1, Symbol: "AAPL", Side: Buy, Type: Limit, Price: 100, Amount: 10}
	order2 := &Order{ID: 2, Symbol: "AAPL", Side: Buy, Type: Limit, Price: 100, Amount: 20}
	pl.AddOrder(order1)
	pl.AddOrder(order2)

	unlinked := &Order{ID: 3, Symbol: "AAPL", Side: Buy, Type: Limit, Price: 100, Amount: 50}
	pl.RemoveOrder(unlinked)

	if pl.Head != order1 || pl.Tail != order2 {
		t.Fatalf("unlinked order corrupted price level queue: head=%v tail=%v", pl.Head, pl.Tail)
	}
	if pl.TotalVolume != 30 {
		t.Fatalf("expected total volume 30, got %d", pl.TotalVolume)
	}
}

func TestOrderBookRejectsMismatchedSymbol(t *testing.T) {
	ob := NewOrderBook("AAPL")
	_, err := ob.ProcessOrder(&Order{ID: 1, Symbol: "TSLA", Side: Buy, Type: Limit, Price: 100, Amount: 10})
	if !errors.Is(err, ErrInvalidOrder) {
		t.Fatalf("expected ErrInvalidOrder for symbol mismatch, got %v", err)
	}
}

func TestRemoveOrderPriceMismatch(t *testing.T) {
	pl := NewPriceLevel(100)
	order1 := &Order{ID: 1, Symbol: "AAPL", Side: Buy, Type: Limit, Price: 100, Amount: 10}
	pl.AddOrder(order1)

	// Order with different price
	wrongPriceOrder := &Order{ID: 2, Symbol: "AAPL", Side: Buy, Type: Limit, Price: 200, Amount: 10}
	pl.RemoveOrder(wrongPriceOrder)

	if pl.TotalVolume != 10 {
		t.Fatalf("expected TotalVolume 10, got %d", pl.TotalVolume)
	}
	if pl.Head != order1 || pl.Tail != order1 {
		t.Fatalf("queue corrupted by removing mismatched price order")
	}
}

func TestDuplicateOrderIDAfterExecution(t *testing.T) {
	ob := NewOrderBook("AAPL")

	// 1. Sell order rests on book
	_, err := ob.ProcessOrder(&Order{ID: 100, Symbol: "AAPL", Side: Sell, Type: Limit, Price: 15000, Amount: 10})
	if err != nil {
		t.Fatalf("unexpected error placing sell: %v", err)
	}

	// 2. Buy order matches fully
	_, err = ob.ProcessOrder(&Order{ID: 101, Symbol: "AAPL", Side: Buy, Type: Limit, Price: 15000, Amount: 10})
	if err != nil {
		t.Fatalf("unexpected error placing buy: %v", err)
	}

	// Neither order is in ob.Orders anymore
	if _, ok := ob.Orders[100]; ok {
		t.Fatalf("order 100 should have been removed after full match")
	}
	if _, ok := ob.Orders[101]; ok {
		t.Fatalf("order 101 should not be in ob.Orders after full match")
	}

	// 3. Re-submitting order 100 must be rejected
	_, err = ob.ProcessOrder(&Order{ID: 100, Symbol: "AAPL", Side: Buy, Type: Limit, Price: 14000, Amount: 5})
	if !errors.Is(err, ErrDuplicateOrderID) {
		t.Fatalf("expected ErrDuplicateOrderID when reusing executed ID 100, got %v", err)
	}

	// 4. Re-submitting order 101 must also be rejected
	_, err = ob.ProcessOrder(&Order{ID: 101, Symbol: "AAPL", Side: Sell, Type: Limit, Price: 16000, Amount: 5})
	if !errors.Is(err, ErrDuplicateOrderID) {
		t.Fatalf("expected ErrDuplicateOrderID when reusing executed ID 101, got %v", err)
	}
}

func TestDuplicateOrderIDAfterCancellation(t *testing.T) {
	ob := NewOrderBook("AAPL")

	_, err := ob.ProcessOrder(&Order{ID: 200, Symbol: "AAPL", Side: Buy, Type: Limit, Price: 14000, Amount: 10})
	if err != nil {
		t.Fatalf("unexpected error placing buy: %v", err)
	}

	if err := ob.CancelOrder(200); err != nil {
		t.Fatalf("unexpected failure cancelling order: %v", err)
	}

	if _, exists := ob.Orders[200]; exists {
		t.Fatalf("order 200 should have been removed from ob.Orders after cancellation")
	}

	// Reusing cancelled ID 200 must be rejected
	_, err = ob.ProcessOrder(&Order{ID: 200, Symbol: "AAPL", Side: Buy, Type: Limit, Price: 14000, Amount: 5})
	if !errors.Is(err, ErrDuplicateOrderID) {
		t.Fatalf("expected ErrDuplicateOrderID when reusing cancelled ID 200, got %v", err)
	}
}

func TestGetOpenOrders(t *testing.T) {
	eng := NewEngine()
	eng.RegisterSymbol("AAPL")
	ob, _ := eng.GetOrderBook("AAPL")

	ob.ProcessOrder(&Order{ID: 1, Symbol: "AAPL", Side: Buy, Type: Limit, Price: 14000, Amount: 5, Timestamp: 1000})
	ob.ProcessOrder(&Order{ID: 2, Symbol: "AAPL", Side: Buy, Type: Limit, Price: 14500, Amount: 10, Timestamp: 2000})
	ob.ProcessOrder(&Order{ID: 3, Symbol: "AAPL", Side: Sell, Type: Limit, Price: 15500, Amount: 8, Timestamp: 3000})

	openOrders := ob.GetOpenOrders()
	if len(openOrders) != 3 {
		t.Fatalf("expected 3 open orders, got %d", len(openOrders))
	}

	ordersFromEng, ok := eng.GetOpenOrders("AAPL")
	if !ok || len(ordersFromEng) != 3 {
		t.Fatalf("expected 3 open orders from engine, got ok=%v, len=%d", ok, len(ordersFromEng))
	}

	// Verify unknown symbol
	_, ok = eng.GetOpenOrders("UNKNOWN")
	if ok {
		t.Fatalf("expected ok=false for unknown symbol open orders")
	}
}

