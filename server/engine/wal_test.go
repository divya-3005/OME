package engine

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWALRecovery(t *testing.T) {
	tempFile := filepath.Join(t.TempDir(), "test_wal.log")

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
	maxID, err := reopenedWAL.Recover(newEngine)
	if err != nil {
		t.Fatalf("failed to recover from WAL: %v", err)
	}
	if maxID != 3 {
		t.Errorf("expected maxID to be 3, got %d", maxID)
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

func TestWALTruncatedRecovery(t *testing.T) {
	tempFile := filepath.Join(t.TempDir(), "test_trunc_wal.log")

	// 1. Write two valid orders
	wal, err := OpenWAL(tempFile)
	if err != nil {
		t.Fatalf("failed to open WAL: %v", err)
	}
	wal.LogPlace(&Order{ID: 1, Symbol: "AAPL", Side: Buy, Type: Limit, Price: 100, Amount: 10})
	wal.LogPlace(&Order{ID: 2, Symbol: "AAPL", Side: Buy, Type: Limit, Price: 105, Amount: 10})
	wal.Close()

	// 2. Simulate unclean crash mid-write by appending partial, corrupt JSON
	f, err := os.OpenFile(tempFile, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatalf("failed to open file for corrupt append: %v", err)
	}
	// Incomplete JSON line cut off mid-write
	_, err = f.Write([]byte(`{"action":"PLACE","order":{"id":3,"symbol":"AA` + "\n"))
	f.Close()
	if err != nil {
		t.Fatalf("failed to write corrupt tail: %v", err)
	}

	// 3. Reopen WAL and recover in new engine (must prune corrupt tail and recover orders 1 & 2)
	engine2 := NewEngine()
	wal2, err := OpenWAL(tempFile)
	if err != nil {
		t.Fatalf("failed to reopen WAL: %v", err)
	}

	maxID, err := wal2.Recover(engine2)
	if err != nil {
		t.Fatalf("unexpected error recovering after corrupt tail: %v", err)
	}
	if maxID != 2 {
		t.Errorf("expected maxID to be 2, got %d", maxID)
	}

	aapl2, exists := engine2.GetOrderBook("AAPL")
	if !exists || len(aapl2.Orders) != 2 {
		t.Fatalf("expected 2 orders recovered in engine2, got %v", aapl2)
	}

	// 4. Log a new order 4 to the same WAL (proving future appends succeed cleanly)
	if err := wal2.LogPlace(&Order{ID: 4, Symbol: "AAPL", Side: Buy, Type: Limit, Price: 110, Amount: 10}); err != nil {
		t.Fatalf("failed to log order 4 to pruned WAL: %v", err)
	}
	wal2.Close()

	// 5. Replay into engine3 (verifies order 4 is fully recoverable)
	engine3 := NewEngine()
	wal3, err := OpenWAL(tempFile)
	if err != nil {
		t.Fatalf("failed to reopen WAL for third recovery: %v", err)
	}
	defer wal3.Close()

	maxID3, err := wal3.Recover(engine3)
	if err != nil {
		t.Fatalf("failed third recovery: %v", err)
	}
	if maxID3 != 4 {
		t.Errorf("expected maxID3 to be 4, got %d", maxID3)
	}

	aapl3, exists := engine3.GetOrderBook("AAPL")
	if !exists || len(aapl3.Orders) != 3 {
		t.Fatalf("expected 3 orders recovered in engine3, got %v", aapl3)
	}
	if _, exists := aapl3.Orders[4]; !exists {
		t.Errorf("expected order 4 to be recovered in engine3")
	}
}

func TestWALNilOrderRecovery(t *testing.T) {
	tempFile := filepath.Join(t.TempDir(), "test_nil_order_wal.log")

	wal, err := OpenWAL(tempFile)
	if err != nil {
		t.Fatalf("failed to open WAL: %v", err)
	}
	wal.LogPlace(&Order{ID: 1, Symbol: "AAPL", Side: Buy, Type: Limit, Price: 100, Amount: 10})
	wal.Close()

	// Append syntactically valid JSON entry with PLACE action but missing order payload
	f, err := os.OpenFile(tempFile, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatalf("failed to open file for append: %v", err)
	}
	_, err = f.Write([]byte(`{"action":"PLACE"}` + "\n"))
	f.Close()
	if err != nil {
		t.Fatalf("failed to write nil order entry: %v", err)
	}

	engine := NewEngine()
	wal2, err := OpenWAL(tempFile)
	if err != nil {
		t.Fatalf("failed to reopen WAL: %v", err)
	}
	defer wal2.Close()

	maxID, err := wal2.Recover(engine)
	if err != nil {
		t.Fatalf("unexpected error recovering after nil order entry: %v", err)
	}
	if maxID != 1 {
		t.Errorf("expected maxID to be 1, got %d", maxID)
	}

	aapl, exists := engine.GetOrderBook("AAPL")
	if !exists || len(aapl.Orders) != 1 {
		t.Fatalf("expected 1 order recovered in engine, got %v", aapl)
	}
}

func TestWALDropsTornFinalRecordAndStaysAppendable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "torn.log")

	wal, err := OpenWAL(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := wal.LogPlace(&Order{ID: 1, Symbol: "AAPL", Side: Buy, Type: Limit, Price: 100, Amount: 10}); err != nil {
		t.Fatal(err)
	}
	wal.Close()

	// A complete JSON object whose trailing newline never reached disk.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(`{"action":"PLACE","order":{"id":2,"symbol":"AAPL","side":0,"type":0,"price":101,"amount":5}}`)
	f.Close()

	eng := NewEngine()
	w2, err := OpenWAL(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w2.Recover(eng); err != nil {
		t.Fatal(err)
	}
	ob, _ := eng.GetOrderBook("AAPL")
	if _, ok := ob.Orders[2]; ok {
		t.Fatal("torn record must not be replayed")
	}
	if err := w2.LogPlace(&Order{ID: 3, Symbol: "AAPL", Side: Buy, Type: Limit, Price: 102, Amount: 5}); err != nil {
		t.Fatal(err)
	}
	w2.Close()

	eng3 := NewEngine()
	w3, err := OpenWAL(path)
	if err != nil {
		t.Fatal(err)
	}
	defer w3.Close()
	if _, err := w3.Recover(eng3); err != nil {
		t.Fatal(err)
	}
	ob3, _ := eng3.GetOrderBook("AAPL")
	if len(ob3.Orders) != 2 {
		t.Fatalf("expected orders 1 and 3 after second recovery, got %d orders", len(ob3.Orders))
	}
}

func TestWALRefusesToTruncateMidFileCorruption(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mid.log")

	w, _ := OpenWAL(path)
	w.LogPlace(&Order{ID: 1, Symbol: "AAPL", Side: Buy, Type: Limit, Price: 100, Amount: 10})
	w.Close()

	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
	f.WriteString("garbage-line\n")
	f.Close()

	w2, _ := OpenWAL(path)
	w2.LogPlace(&Order{ID: 2, Symbol: "AAPL", Side: Buy, Type: Limit, Price: 101, Amount: 10})
	w2.Close()

	before, _ := os.Stat(path)

	w3, _ := OpenWAL(path)
	defer w3.Close()
	if _, err := w3.Recover(NewEngine()); err == nil {
		t.Fatal("expected Recover to fail on mid-file corruption")
	}
	after, _ := os.Stat(path)
	if after.Size() != before.Size() {
		t.Fatalf("WAL must not be truncated on mid-file corruption: %d -> %d bytes", before.Size(), after.Size())
	}
}

func TestWALFailsClosedAfterWriteError(t *testing.T) {
	w, err := OpenWAL(filepath.Join(t.TempDir(), "broken.log"))
	if err != nil {
		t.Fatal(err)
	}
	w.file.Close() // force every write (and the rollback) to fail

	err = w.LogPlace(&Order{ID: 1, Symbol: "AAPL", Side: Buy, Type: Limit, Price: 100, Amount: 1})
	if !errors.Is(err, ErrWAL) {
		t.Fatalf("expected ErrWAL, got %v", err)
	}
	err = w.LogPlace(&Order{ID: 2, Symbol: "AAPL", Side: Buy, Type: Limit, Price: 100, Amount: 1})
	if err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("expected WAL to stay disabled after an unrecoverable failure, got %v", err)
	}
}

func TestWALInvalidOrderPayloadRecovery(t *testing.T) {
	tempFile := filepath.Join(t.TempDir(), "invalid_order_wal.log")

	wal, err := OpenWAL(tempFile)
	if err != nil {
		t.Fatalf("failed to open WAL: %v", err)
	}
	wal.LogPlace(&Order{ID: 1, Symbol: "AAPL", Side: Buy, Type: Limit, Price: 100, Amount: 10})
	wal.Close()

	// Append valid JSON with action PLACE but invalid order payload (amount: 0, price: 0)
	f, err := os.OpenFile(tempFile, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatalf("failed to open file for append: %v", err)
	}
	_, err = f.Write([]byte(`{"action":"PLACE","order":{"id":2,"symbol":"AAPL","side":0,"type":0,"price":0,"amount":0}}` + "\n"))
	f.Close()
	if err != nil {
		t.Fatalf("failed to write invalid payload line: %v", err)
	}

	engine := NewEngine()
	wal2, err := OpenWAL(tempFile)
	if err != nil {
		t.Fatalf("failed to reopen WAL: %v", err)
	}
	defer wal2.Close()

	maxID, err := wal2.Recover(engine)
	if err != nil {
		t.Fatalf("unexpected error recovering after invalid order payload: %v", err)
	}
	if maxID != 1 {
		t.Errorf("expected maxID to be 1, got %d", maxID)
	}

	aapl, exists := engine.GetOrderBook("AAPL")
	if !exists || len(aapl.Orders) != 1 {
		t.Fatalf("expected 1 order recovered in engine, got %v", aapl)
	}

	// Verify WAL is still cleanly appendable
	if err := wal2.LogPlace(&Order{ID: 3, Symbol: "AAPL", Side: Buy, Type: Limit, Price: 105, Amount: 5}); err != nil {
		t.Fatalf("failed to log order 3 to pruned WAL: %v", err)
	}
}

func TestWALCreatesParentDirectories(t *testing.T) {
	nestedPath := filepath.Join(t.TempDir(), "nested", "sub", "wal.log")
	wal, err := OpenWAL(nestedPath)
	if err != nil {
		t.Fatalf("expected OpenWAL to create parent directories, got error: %v", err)
	}
	defer wal.Close()

	if err := wal.LogPlace(&Order{ID: 1, Symbol: "AAPL", Side: Buy, Type: Limit, Price: 100, Amount: 10}); err != nil {
		t.Fatalf("failed to log order in nested WAL: %v", err)
	}
}


