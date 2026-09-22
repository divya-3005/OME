package engine

import (
	"os"
	"testing"
)

func TestWALRecovery(t *testing.T) {
	tempFile := "test_wal.log"
	defer os.Remove(tempFile)

	// 1. Create WAL and log actions
	wal, err := OpenWAL(tempFile)
	if err != nil {
		t.Fatalf("failed to open WAL: %v", err)
	}

	wal.LogPlace(&Order{ID: 1, Symbol: "AAPL", Side: Buy, Type: Limit, Price: 100, Amount: 10})
	wal.LogPlace(&Order{ID: 2, Symbol: "AAPL", Side: Sell, Type: Limit, Price: 100, Amount: 5})
	wal.LogPlace(&Order{ID: 3, Symbol: "TSLA", Side: Buy, Type: Limit, Price: 200, Amount: 10})
	wal.LogCancel("TSLA", 3)

	wal.Close()

	// 2. Simulate server restart with a brand new engine
	newEngine := NewEngine()

	reopenedWAL, err := OpenWAL(tempFile)
	if err != nil {
		t.Fatalf("failed to reopen WAL: %v", err)
	}
	defer reopenedWAL.Close()

	// 3. Replay WAL into new engine
	if err := reopenedWAL.Recover(newEngine); err != nil {
		t.Fatalf("failed to recover from WAL: %v", err)
	}

	// 4. Verify AAPL state (5 shares remaining on Order 1)
	aaplBook, exists := newEngine.GetOrderBook("AAPL")
	if !exists {
		t.Fatalf("expected AAPL orderbook to exist")
	}
	order1, exists := aaplBook.Orders[1]
	if !exists || order1.Amount != 5 {
		t.Errorf("expected Order 1 to be restored with 5 remaining shares, got %v", order1)
	}

	// 5. Verify TSLA state (Order 3 was cancelled, so 0 orders)
	tslaBook, exists := newEngine.GetOrderBook("TSLA")
	if !exists {
		t.Fatalf("expected TSLA orderbook to exist")
	}
	if len(tslaBook.Orders) != 0 {
		t.Errorf("expected 0 orders in TSLA book, got %d", len(tslaBook.Orders))
	}
}
