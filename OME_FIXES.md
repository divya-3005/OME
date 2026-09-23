# OME — Exact Fix Spec (for Antigravity)

## Instructions to the agent

- Apply the fixes **in phase order** (Phase 1 → Phase 6). Each fix names the file, what is wrong, and the exact change.
- Do **not** make any change that is not listed here. No refactors, renames or "improvements" beyond this spec.
- After **every phase**, run from `server/`:
  ```bash
  gofmt -l .          # must print nothing
  go vet ./...
  go test -race ./...
  ```
  Do not start the next phase until all three pass. If a change here does not compile or conflicts with the actual code, stop and report it instead of guessing.
- Where this spec says "replace function X", replace the whole function body, keeping its position in the file.

---

## PHASE 1 — Engine (`server/engine/`)

### Fix 1.1 — Add typed errors and hard limits (supports fixes 1.3, 1.4, 2.3, 2.4)

**New file `server/engine/errors.go`:**

```go
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
```

### Fix 1.2 — Correct the misleading `Timestamp` comment (`order.go`)

Replace line 27:
```go
Timestamp int64     `json:"timestamp"` // Unix timestamp in nanoseconds for FIFO priority
```
with:
```go
Timestamp int64     `json:"timestamp"` // Server-assigned arrival time (ns). Informational only: priority within a price level is arrival order.
```

### Fix 1.3 — Order validation with typed errors and bounds (`order_book.go`)

Add this method (anywhere in the file):

```go
// validate checks admission rules. Caller must hold ob.mu.
func (ob *OrderBook) validate(order *Order) error {
	if order == nil {
		return fmt.Errorf("%w: order cannot be nil", ErrInvalidOrder)
	}
	if order.ID == 0 {
		return fmt.Errorf("%w: order ID must be positive", ErrInvalidOrder)
	}
	if _, exists := ob.Orders[order.ID]; exists {
		return fmt.Errorf("%w: %d", ErrDuplicateOrderID, order.ID)
	}
	if order.Side != Buy && order.Side != Sell {
		return fmt.Errorf("%w: invalid side %d (must be 0 for Buy or 1 for Sell)", ErrInvalidOrder, order.Side)
	}
	if order.Type != Limit && order.Type != Market {
		return fmt.Errorf("%w: invalid type %d (must be 0 for Limit or 1 for Market)", ErrInvalidOrder, order.Type)
	}
	if order.Amount == 0 || order.Amount > MaxOrderAmount {
		return fmt.Errorf("%w: amount must be between 1 and %d", ErrInvalidOrder, MaxOrderAmount)
	}
	if order.Type == Limit && (order.Price == 0 || order.Price > MaxOrderPrice) {
		return fmt.Errorf("%w: limit price must be between 1 and %d", ErrInvalidOrder, MaxOrderPrice)
	}
	return nil
}
```

### Fix 1.4 — Publish events inside the book lock (fixes out-of-order broadcasts and the stale book) (`order_book.go`)

**Why:** broadcasts currently happen after the lock is released, so two concurrent orders on one symbol can be broadcast in the opposite order they executed. The server also never announces newly resting orders, so the UI book goes stale.

Replace `ProcessOrderWithWAL` (lines 210–251) with these two functions:

```go
// ProcessOrderWithWAL validates, persists (write + fsync) and matches an order under the book lock.
func (ob *OrderBook) ProcessOrderWithWAL(order *Order, wal *WAL) ([]*Trade, error) {
	return ob.ProcessOrderWithWALNotify(order, wal, nil)
}

// ProcessOrderWithWALNotify is ProcessOrderWithWAL plus an optional notify callback.
// notify runs while the book lock is still held, so events for one symbol are published
// in exactly the order they executed. It is called only if the order produced trades or
// left a resting remainder. notify must not call back into this OrderBook.
func (ob *OrderBook) ProcessOrderWithWALNotify(order *Order, wal *WAL, notify func(trades []*Trade, rested bool)) ([]*Trade, error) {
	ob.mu.Lock()
	defer ob.mu.Unlock()

	if err := ob.validate(order); err != nil {
		return nil, err
	}

	// LogPlace writes AND fsyncs; it returns an error wrapping ErrWAL on any failure,
	// in which case nothing is applied to the book.
	if wal != nil {
		if err := wal.LogPlace(order); err != nil {
			return nil, err
		}
	}

	var trades []*Trade
	if order.Side == Buy {
		trades = ob.matchBuyOrder(order)
	} else {
		trades = ob.matchSellOrder(order)
	}

	rested := order.Type == Limit && order.Amount > 0
	if notify != nil && (len(trades) > 0 || rested) {
		notify(trades, rested)
	}
	return trades, nil
}
```

Replace `CancelOrderWithWAL` (lines 258–307) with:

```go
// CancelOrderWithWAL cancels a resting order with WAL persistence under the book lock.
func (ob *OrderBook) CancelOrderWithWAL(orderID uint64, wal *WAL) (bool, error) {
	return ob.CancelOrderWithWALNotify(orderID, wal, nil)
}

// CancelOrderWithWALNotify is CancelOrderWithWAL plus a notify callback run under the book lock
// after a successful cancel.
func (ob *OrderBook) CancelOrderWithWALNotify(orderID uint64, wal *WAL, notify func()) (bool, error) {
	ob.mu.Lock()
	defer ob.mu.Unlock()

	order, exists := ob.Orders[orderID]
	if !exists {
		return false, fmt.Errorf("%w: %d", ErrOrderNotFound, orderID)
	}

	if wal != nil {
		if err := wal.LogCancel(ob.Symbol, orderID); err != nil {
			return false, err
		}
	}

	// >>> KEEP THE EXISTING UNLINK BLOCK HERE UNCHANGED <<<
	// (the `if order.Side == Buy { ... } else { ... }` block from the old lines 277–303)

	delete(ob.Orders, orderID)
	if notify != nil {
		notify()
	}
	return true, nil
}
```

Add a top-of-book accessor that reports each side separately (needed by Fix 3.2):

```go
// TopOfBook returns the best bid and best ask independently.
func (ob *OrderBook) TopOfBook() (bid uint64, hasBid bool, ask uint64, hasAsk bool) {
	ob.mu.RLock()
	defer ob.mu.RUnlock()
	if len(ob.Bids) > 0 {
		bid, hasBid = ob.Bids[0].Price, true
	}
	if len(ob.Asks) > 0 {
		ask, hasAsk = ob.Asks[0].Price, true
	}
	return
}
```

Keep `ProcessOrder`, `CancelOrder`, `HasOrder`, `GetSnapshot`, `GetBestBidAsk` as they are.

### Fix 1.5 — Engine routing with typed errors and notify variants (`engine.go`)

Replace `ProcessOrderWithWAL`, `CancelOrderWithWAL` (lines 176–203) with:

```go
// ProcessOrderWithWALNotify routes an order to its book. See OrderBook.ProcessOrderWithWALNotify.
func (e *Engine) ProcessOrderWithWALNotify(order *Order, wal *WAL, notify func(trades []*Trade, rested bool)) ([]*Trade, error) {
	if order == nil {
		return nil, fmt.Errorf("%w: order cannot be nil", ErrInvalidOrder)
	}
	ob, exists := e.GetOrderBook(order.Symbol)
	if !exists {
		return nil, fmt.Errorf("%w: %q", ErrUnknownSymbol, order.Symbol)
	}
	return ob.ProcessOrderWithWALNotify(order, wal, notify)
}

// ProcessOrderWithWAL routes an order to its book with WAL persistence.
func (e *Engine) ProcessOrderWithWAL(order *Order, wal *WAL) ([]*Trade, error) {
	return e.ProcessOrderWithWALNotify(order, wal, nil)
}

// CancelOrderWithWALNotify cancels an order in the given symbol's book.
func (e *Engine) CancelOrderWithWALNotify(symbol string, orderID uint64, wal *WAL, notify func()) (bool, error) {
	ob, exists := e.GetOrderBook(symbol)
	if !exists {
		return false, fmt.Errorf("%w: %q", ErrUnknownSymbol, symbol)
	}
	return ob.CancelOrderWithWALNotify(orderID, wal, notify)
}

// CancelOrderWithWAL cancels an order with WAL persistence.
func (e *Engine) CancelOrderWithWAL(symbol string, orderID uint64, wal *WAL) (bool, error) {
	return e.CancelOrderWithWALNotify(symbol, orderID, wal, nil)
}
```

Keep `ProcessOrder` and `CancelOrder` as they are.

### Fix 1.6 — WAL: atomic write+fsync with rollback, fail-stop, safe recovery (`wal.go`)

**What is wrong now:**
1. A short/failed `Write` leaves partial bytes; the next append lands on the same line, and recovery later truncates every acknowledged record after it.
2. `Write` succeeding but `Sync` failing leaves a record for an order the client was told was rejected.
3. A complete JSON record missing its trailing `\n` is accepted, never repaired, and the next append concatenates onto it.
4. Recovery silently truncates everything after the *first* bad line, even valid records.

**Replace the entire contents of `wal.go` with:**

```go
package engine

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"sync"
)

// WALEntry represents a single logged event in the Write-Ahead Log.
type WALEntry struct {
	Action  string `json:"action"` // "PLACE" or "CANCEL"
	Order   *Order `json:"order,omitempty"`
	OrderID uint64 `json:"order_id,omitempty"`
	Symbol  string `json:"symbol,omitempty"`
}

func (e *WALEntry) validate() error {
	switch e.Action {
	case "PLACE":
		if e.Order == nil {
			return errors.New("PLACE entry has no order payload")
		}
	case "CANCEL":
		if e.Symbol == "" || e.OrderID == 0 {
			return errors.New("CANCEL entry missing symbol or order_id")
		}
	default:
		return fmt.Errorf("unknown action %q", e.Action)
	}
	return nil
}

// WAL manages the append-only log file on disk.
type WAL struct {
	mu     sync.Mutex
	file   *os.File
	size   int64 // length of the known-good prefix of the file
	broken error // once set, the log can no longer be trusted and every append fails
}

// OpenWAL opens or creates the WAL log file.
func OpenWAL(path string) (*WAL, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_RDWR, 0644)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, err
	}
	return &WAL{file: file, size: info.Size()}, nil
}

// LogPlace durably records an order placement (write + fsync).
func (w *WAL) LogPlace(order *Order) error {
	return w.append(WALEntry{Action: "PLACE", Order: order})
}

// LogCancel durably records an order cancellation (write + fsync).
func (w *WAL) LogCancel(symbol string, orderID uint64) error {
	return w.append(WALEntry{Action: "CANCEL", Symbol: symbol, OrderID: orderID})
}

// append writes one entry and fsyncs it while holding the WAL lock, so no other
// entry can interleave. The entry is durable only if this returns nil. On failure
// the file is rolled back to its previous length so no partial or unacknowledged
// record is left behind.
func (w *WAL) append(entry WALEntry) error {
	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("%w: encode: %w", ErrWAL, err)
	}
	data = append(data, '\n')

	w.mu.Lock()
	defer w.mu.Unlock()

	if w.broken != nil {
		return fmt.Errorf("%w: log disabled after earlier failure: %w", ErrWAL, w.broken)
	}

	start := w.size
	if _, err := w.file.Write(data); err != nil {
		if rbErr := w.rollback(start); rbErr != nil {
			w.broken = fmt.Errorf("write failed (%v) and rollback failed: %w", err, rbErr)
		}
		return fmt.Errorf("%w: write: %w", ErrWAL, err)
	}

	if err := w.file.Sync(); err != nil {
		// After a failed fsync the on-disk state is unknown (the kernel may already have
		// dropped the dirty pages). Roll back best-effort and stop accepting writes; the
		// process must restart and recover from what is actually on disk.
		_ = w.rollback(start)
		w.broken = fmt.Errorf("fsync failed: %w", err)
		return fmt.Errorf("%w: fsync: %w", ErrWAL, err)
	}

	w.size = start + int64(len(data))
	return nil
}

func (w *WAL) rollback(offset int64) error {
	if err := w.file.Truncate(offset); err != nil {
		return err
	}
	return w.file.Sync()
}

// Recover replays the log into eng.
//   - An incomplete final record (no trailing newline) was never fsynced or acknowledged: it is dropped.
//   - A corrupt final record is dropped.
//   - A corrupt record with more data after it is NOT truncated: Recover returns an error,
//     because truncating would destroy acknowledged records. The caller must refuse to start.
func (w *WAL) Recover(eng *Engine) (uint64, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if _, err := w.file.Seek(0, io.SeekStart); err != nil {
		return 0, err
	}

	reader := bufio.NewReader(w.file)
	var validOffset int64
	var maxOrderID uint64

	for {
		line, readErr := reader.ReadBytes('\n')
		if readErr != nil && readErr != io.EOF {
			return maxOrderID, fmt.Errorf("WAL read error at offset %d: %w", validOffset, readErr)
		}
		if len(line) == 0 {
			break // clean end of file
		}
		if line[len(line)-1] != '\n' {
			log.Printf("WAL recovery: dropping incomplete final record at offset %d (%d bytes)", validOffset, len(line))
			break
		}

		trimmed := bytes.TrimRight(line, "\r\n")
		if len(trimmed) > 0 {
			var entry WALEntry
			err := json.Unmarshal(trimmed, &entry)
			if err == nil {
				err = entry.validate()
			}
			if err != nil {
				if _, peekErr := reader.Peek(1); peekErr == nil {
					return maxOrderID, fmt.Errorf("WAL corrupt at offset %d with more records after it; refusing to truncate acknowledged data: %w", validOffset, err)
				}
				log.Printf("WAL recovery: dropping corrupt final record at offset %d: %v", validOffset, err)
				break
			}

			switch entry.Action {
			case "PLACE":
				eng.RegisterSymbol(entry.Order.Symbol)
				if entry.Order.ID > maxOrderID {
					maxOrderID = entry.Order.ID
				}
				if _, err := eng.ProcessOrder(entry.Order); err != nil {
					log.Printf("WAL recovery: warning replaying order %d: %v", entry.Order.ID, err)
				}
			case "CANCEL":
				if entry.OrderID > maxOrderID {
					maxOrderID = entry.OrderID
				}
				if _, err := eng.CancelOrder(entry.Symbol, entry.OrderID); err != nil {
					log.Printf("WAL recovery: warning replaying cancel for order %d: %v", entry.OrderID, err)
				}
			}
		}
		validOffset += int64(len(line))
	}

	if err := w.file.Truncate(validOffset); err != nil {
		return maxOrderID, fmt.Errorf("failed to truncate WAL tail: %w", err)
	}
	if err := w.file.Sync(); err != nil {
		return maxOrderID, fmt.Errorf("failed to sync WAL after truncation: %w", err)
	}
	if _, err := w.file.Seek(validOffset, io.SeekStart); err != nil {
		return maxOrderID, fmt.Errorf("failed to seek to WAL tail: %w", err)
	}
	w.size = validOffset

	eng.SetMinOrderID(maxOrderID + 1)
	return maxOrderID, nil
}

// Sync commits the current contents of the WAL file to stable storage.
// (LogPlace/LogCancel already fsync; this is kept for API compatibility.)
func (w *WAL) Sync() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.file.Sync()
}

// Close closes the WAL file.
func (w *WAL) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.file.Close()
}
```

### Fix 1.7 — Benchmarks must actually measure matching (`order_book_test.go`)

**What is wrong:** replenishing sells go in at 100–109, but the buy only crosses up to 105. After ~15k iterations the asks ≤105 are gone, and from then on the buy rests instead of matching (~0.4% fill rate at b.N ≈ 3.4M).

Replace the timed loop in `BenchmarkProcessOrder` (lines 187–212) with:

```go
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
```

Replace the timed loop in `BenchmarkProcessOrderWithWAL` (lines 234–245) with the same pattern, using `ob.ProcessOrderWithWAL(..., wal)` for both calls.

### Fix 1.8 — New WAL tests (`wal_test.go`)

Add imports `errors`, `path/filepath`, `strings` and append these tests:

```go
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
```

Also add to `order_book_test.go`:

```go
func TestRejectsOversizedAmount(t *testing.T) {
	ob := NewOrderBook("AAPL")
	_, err := ob.ProcessOrder(&Order{ID: 1, Symbol: "AAPL", Side: Buy, Type: Limit, Price: 100, Amount: MaxOrderAmount + 1})
	if !errors.Is(err, ErrInvalidOrder) {
		t.Fatalf("expected ErrInvalidOrder, got %v", err)
	}
}
```
(add `errors` to that file's imports).

---

## PHASE 2 — HTTP server & WebSocket hub (`server/main.go`, `server/hub.go`)

### Fix 2.1 — One JSON event per WebSocket frame (`hub.go`)

**What is wrong:** `writePump` concatenates queued messages into one frame separated by `\n`; the browser does `JSON.parse` on the whole frame, which throws, and every event in that frame is lost.

Replace the `case message, ok := <-c.send:` branch in `writePump` (lines 90–114) with:

```go
		case message, ok := <-c.send:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				// Hub closed the channel
				c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			// Exactly one JSON event per WebSocket frame.
			if err := c.conn.WriteMessage(websocket.TextMessage, message); err != nil {
				return
			}
```

### Fix 2.2 — Reusable origin check (`hub.go`)

Replace the `upgrader` declaration (lines 23–47) with:

```go
// isAllowedOrigin reports whether a request's Origin is trusted. Requests without an
// Origin header (curl, bots, tests) are allowed; browsers always send Origin on
// WebSocket upgrades and on non-GET requests.
func isAllowedOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	if u.Host == r.Host || u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" {
		return true
	}
	if allowed := os.Getenv("ALLOWED_ORIGINS"); allowed != "" {
		for _, a := range strings.Split(allowed, ",") {
			if strings.TrimSpace(a) == origin {
				return true
			}
		}
	}
	return false
}

var upgrader = websocket.Upgrader{CheckOrigin: isAllowedOrigin}
```

(`TestCheckOrigin` calls `upgrader.CheckOrigin` and keeps working unchanged.)

### Fix 2.3 — Rewrite `main.go` handlers and startup

**What this fixes:** client-supplied IDs colliding with generated ones (#7), `order.amount` returning the remaining quantity (#8), wrong status codes (#9), broadcasts outside the lock (#11), missing CSRF protection on REST (#12), `defer wal.Close()` never running (#13), client-supplied timestamps (#14), recovery errors being ignored, and the one-sided seeding check.

**Replace the entire contents of `main.go` with:**

```go
package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"mime"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/divya-3005/OME/server/engine"
)

var supportedSymbols = []string{"AAPL", "TSLA", "BTC-USD"}

func main() {
	eng := engine.NewEngine()
	eng.SetMinOrderID(uint64(time.Now().UnixMilli()))

	hub := NewHub()
	go hub.Run()

	for _, sym := range supportedSymbols {
		eng.RegisterSymbol(sym)
	}

	wal, err := engine.OpenWAL("wal.log")
	if err != nil {
		log.Fatalf("failed to open WAL: %v", err)
	}
	maxID, err := wal.Recover(eng)
	if err != nil {
		wal.Close()
		log.Fatalf("WAL recovery failed, refusing to start: %v", err)
	}
	log.Printf("WAL recovery complete: restored state (highest order ID: %d)", maxID)

	sim := NewMarketSimulator(eng, hub, wal)
	for _, sym := range supportedSymbols {
		sim.EnsureLiquidity(sym)
	}
	sim.Start()
	log.Println("Market Simulator active: simulating live institutional order flow")

	mux := http.NewServeMux()
	mux.HandleFunc("POST /order", requireAllowedOrigin(handlePlaceOrder(eng, hub, wal)))
	mux.HandleFunc("DELETE /order", requireAllowedOrigin(handleCancelOrder(eng, hub, wal)))
	mux.HandleFunc("GET /orderbook", handleGetOrderBook(eng))
	mux.HandleFunc("/ws", handleWebSocket(hub))
	mux.HandleFunc("POST /simulator/toggle", requireAllowedOrigin(func(w http.ResponseWriter, r *http.Request) {
		running := sim.Toggle()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]bool{"running": running})
	}))
	mux.HandleFunc("GET /simulator/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]bool{"running": sim.IsRunning()})
	})
	mux.Handle("/", http.FileServer(http.Dir("./public")))

	srv := &http.Server{Addr: ":8080", Handler: mux}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	serverErr := make(chan error, 1)
	go func() { serverErr <- srv.ListenAndServe() }()
	log.Println("Order Matching Engine running on http://localhost:8080")
	log.Println("WebSocket stream available at ws://localhost:8080/ws")

	exitCode := 0
	select {
	case <-ctx.Done():
		log.Println("shutdown signal received")
	case err := <-serverErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("HTTP server error: %v", err)
			exitCode = 1
		}
	}

	// Order matters: stop accepting requests, stop the bot, then close the WAL.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("HTTP shutdown: %v", err)
	}
	sim.Stop()
	if err := wal.Close(); err != nil {
		log.Printf("WAL close: %v", err)
		exitCode = 1
	}
	if exitCode != 0 {
		os.Exit(exitCode)
	}
}

// requireAllowedOrigin rejects state-changing requests coming from untrusted browser origins.
func requireAllowedOrigin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !isAllowedOrigin(r) {
			http.Error(w, "origin not allowed", http.StatusForbidden)
			return
		}
		next(w, r)
	}
}

func isJSONRequest(r *http.Request) bool {
	mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	return err == nil && mt == "application/json"
}

// writeEngineError maps engine errors to HTTP status codes.
func writeEngineError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, engine.ErrInvalidOrder), errors.Is(err, engine.ErrUnknownSymbol):
		status = http.StatusBadRequest
	case errors.Is(err, engine.ErrDuplicateOrderID):
		status = http.StatusConflict
	case errors.Is(err, engine.ErrOrderNotFound):
		status = http.StatusNotFound
	case errors.Is(err, engine.ErrWAL):
		status = http.StatusServiceUnavailable
	}
	http.Error(w, err.Error(), status)
}

// publishOrderEvents returns a notify callback that broadcasts trades and book updates.
// It runs under the order book lock, so events are published in execution order.
func publishOrderEvents(hub *Hub, symbol string) func([]*engine.Trade, bool) {
	return func(trades []*engine.Trade, rested bool) {
		if len(trades) > 0 {
			hub.BroadcastJSON(map[string]interface{}{
				"type":   "trades",
				"symbol": symbol,
				"data":   trades,
			})
		}
		if rested {
			hub.BroadcastJSON(map[string]interface{}{
				"type":   "book_update",
				"symbol": symbol,
			})
		}
	}
}

// handleWebSocket upgrades incoming HTTP connections to WebSocket and registers a non-blocking Client
func handleWebSocket(hub *Hub) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Printf("failed to upgrade websocket: %v", err)
			return
		}
		client := &Client{
			hub:  hub,
			conn: conn,
			send: make(chan []byte, sendBufferSize),
		}
		hub.register <- client
		go client.writePump()
		go client.readPump()
	}
}

// handlePlaceOrder processes POST /order.
func handlePlaceOrder(eng *engine.Engine, hub *Hub, wal *engine.WAL) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !isJSONRequest(r) {
			http.Error(w, "Content-Type must be application/json", http.StatusUnsupportedMediaType)
			return
		}

		var order engine.Order
		if err := json.NewDecoder(r.Body).Decode(&order); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}

		// Order IDs and timestamps are always server-assigned so they can never collide.
		if order.ID != 0 {
			http.Error(w, "id must not be supplied; the server assigns order IDs", http.StatusBadRequest)
			return
		}
		order.ID = eng.NextOrderID()
		order.Timestamp = time.Now().UnixNano()

		submitted := order // snapshot before matching mutates Amount
		trades, err := eng.ProcessOrderWithWALNotify(&order, wal, publishOrderEvents(hub, order.Symbol))
		if err != nil {
			writeEngineError(w, err)
			return
		}

		var filledAmount uint64
		for _, t := range trades {
			filledAmount += t.Amount
		}
		remainingAmount := submitted.Amount - filledAmount

		status := "FILLED"
		if remainingAmount > 0 {
			if order.Type == engine.Market {
				if filledAmount == 0 {
					status = "UNFILLED"
				} else {
					status = "PARTIALLY_FILLED"
				}
			} else {
				if filledAmount == 0 {
					status = "RESTING"
				} else {
					status = "PARTIALLY_FILLED_RESTING"
				}
			}
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"order":            submitted,
			"trades":           trades,
			"requested_amount": submitted.Amount,
			"filled_amount":    filledAmount,
			"remaining_amount": remainingAmount,
			"status":           status,
		})
	}
}

// handleCancelOrder processes DELETE /order?symbol=AAPL&id=1.
func handleCancelOrder(eng *engine.Engine, hub *Hub, wal *engine.WAL) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		symbol := r.URL.Query().Get("symbol")
		idStr := r.URL.Query().Get("id")

		orderID, err := strconv.ParseUint(idStr, 10, 64)
		if err != nil || symbol == "" {
			http.Error(w, "query params 'symbol' and 'id' are required", http.StatusBadRequest)
			return
		}

		success, err := eng.CancelOrderWithWALNotify(symbol, orderID, wal, func() {
			hub.BroadcastJSON(map[string]interface{}{
				"type":     "order_cancelled",
				"symbol":   symbol,
				"order_id": orderID,
			})
		})
		if err != nil {
			writeEngineError(w, err)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success":  success,
			"order_id": orderID,
		})
	}
}

// handleGetOrderBook returns a snapshot of bids and asks for GET /orderbook?symbol=AAPL
func handleGetOrderBook(eng *engine.Engine) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		symbol := r.URL.Query().Get("symbol")
		ob, exists := eng.GetOrderBook(symbol)
		if !exists {
			http.Error(w, "symbol not found", http.StatusNotFound)
			return
		}
		bids, asks := ob.GetSnapshot()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"symbol": symbol,
			"bids":   bids,
			"asks":   asks,
		})
	}
}
```

### Fix 2.4 — Update existing server tests + add new ones (`main_test.go`, `hub_test.go`)

1. In **every** `httptest.NewRequest("POST", "/order", ...)` in `main_test.go` (including the invalid-JSON case), add right after it:
   ```go
   req.Header.Set("Content-Type", "application/json")
   ```

2. Add to `main_test.go` (add `strings` to imports):

```go
func TestPlaceOrderRejectsClientSuppliedID(t *testing.T) {
	eng, hub, wal, cleanup := setupTestServer(t)
	defer cleanup()
	body := `{"id":101,"symbol":"AAPL","side":0,"type":0,"price":15000,"amount":1}`
	req := httptest.NewRequest("POST", "/order", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handlePlaceOrder(eng, hub, wal)(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for client-supplied id, got %d", rec.Code)
	}
}

func TestPlaceOrderResponseReportsSubmittedAmount(t *testing.T) {
	eng, hub, wal, cleanup := setupTestServer(t)
	defer cleanup()
	h := handlePlaceOrder(eng, hub, wal)
	post := func(body string) map[string]interface{} {
		req := httptest.NewRequest("POST", "/order", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h(rec, req)
		var resp map[string]interface{}
		json.Unmarshal(rec.Body.Bytes(), &resp)
		return resp
	}
	post(`{"symbol":"AAPL","side":1,"type":0,"price":15000,"amount":4}`)
	resp := post(`{"symbol":"AAPL","side":0,"type":0,"price":15000,"amount":10}`)
	if got := resp["order"].(map[string]interface{})["amount"].(float64); got != 10 {
		t.Errorf("order.amount must be the submitted amount 10, got %v", got)
	}
	if resp["remaining_amount"].(float64) != 6 || resp["status"] != "PARTIALLY_FILLED_RESTING" {
		t.Errorf("unexpected remaining/status: %v / %v", resp["remaining_amount"], resp["status"])
	}
}

func TestPlaceOrderRequiresJSONContentType(t *testing.T) {
	eng, hub, wal, cleanup := setupTestServer(t)
	defer cleanup()
	req := httptest.NewRequest("POST", "/order", strings.NewReader(`{"symbol":"AAPL","side":0,"type":0,"price":1,"amount":1}`))
	req.Header.Set("Content-Type", "text/plain")
	rec := httptest.NewRecorder()
	handlePlaceOrder(eng, hub, wal)(rec, req)
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("expected 415, got %d", rec.Code)
	}
}

func TestStateChangingRoutesRejectForeignOrigin(t *testing.T) {
	eng, hub, wal, cleanup := setupTestServer(t)
	defer cleanup()
	h := requireAllowedOrigin(handlePlaceOrder(eng, hub, wal))
	req := httptest.NewRequest("POST", "/order", strings.NewReader(`{"symbol":"AAPL","side":0,"type":0,"price":1,"amount":1}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://evil.example")
	req.Host = "localhost:8080"
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
}

func TestPlaceOrderWALFailureReturns503(t *testing.T) {
	eng, hub, wal, cleanup := setupTestServer(t)
	defer cleanup()
	wal.Close() // every subsequent WAL write fails
	req := httptest.NewRequest("POST", "/order", strings.NewReader(`{"symbol":"AAPL","side":0,"type":0,"price":15000,"amount":1}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handlePlaceOrder(eng, hub, wal)(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 on WAL failure, got %d", rec.Code)
	}
}
```

3. Add to `hub_test.go`:

```go
func TestWebSocketOneEventPerFrame(t *testing.T) {
	hub := NewHub()
	go hub.Run()
	server := httptest.NewServer(handleWebSocket(hub))
	defer server.Close()

	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	time.Sleep(20 * time.Millisecond)

	for i := 0; i < 20; i++ {
		hub.BroadcastJSON(map[string]int{"seq": i})
	}
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	for i := 0; i < 20; i++ {
		_, p, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read %d: %v", i, err)
		}
		var m map[string]int
		if err := json.Unmarshal(p, &m); err != nil {
			t.Fatalf("frame %d is not a single JSON object: %q", i, p)
		}
		if m["seq"] != i {
			t.Fatalf("out of order: got %d want %d", m["seq"], i)
		}
	}
}
```

---

## PHASE 3 — Market simulator (`server/simulator.go`)

### Fix 3.1 + 3.2 — Race-free start/stop, and self-healing liquidity

**What is wrong:**
- The goroutine reads `sim.stop` without the mutex while `Start()` reassigns it. A fast off→on toggle can leave the old goroutine running on the new channel, so two bots run.
- If one side of a book empties, `GetBestBidAsk` returns `!ok` forever and that symbol stops trading. Startup only re-seeds when **bids** are empty.

**Replace the entire contents of `simulator.go` with:**

```go
package main

import (
	"log"
	"math/rand"
	"sync"
	"time"

	"github.com/divya-3005/OME/server/engine"
)

type MarketSimulator struct {
	eng *engine.Engine
	hub *Hub
	wal *engine.WAL

	mu      sync.Mutex
	running bool
	stop    chan struct{} // closed to stop the current run
	done    chan struct{} // closed by the current run's goroutine when it exits
}

func NewMarketSimulator(eng *engine.Engine, hub *Hub, wal *engine.WAL) *MarketSimulator {
	return &MarketSimulator{eng: eng, hub: hub, wal: wal}
}

func referenceMid(symbol string) (uint64, bool) {
	switch symbol {
	case "AAPL":
		return 15000, true // $150.00
	case "TSLA":
		return 24000, true // $240.00
	case "BTC-USD":
		return 6400000, true // $64,000.00
	}
	return 0, false
}

// placeLadder rests 10 limit orders stepping away from anchor by 10 ticks per level
// (below anchor for buys, above anchor for sells).
func (sim *MarketSimulator) placeLadder(symbol string, side engine.Side, anchor uint64) {
	for i := 1; i <= 10; i++ {
		diff := uint64(i * 10)
		var price uint64
		if side == engine.Buy {
			if anchor <= diff {
				break // never place a zero or wrapped-around price
			}
			price = anchor - diff
		} else {
			price = anchor + diff
		}
		order := &engine.Order{
			ID:        sim.eng.NextOrderID(),
			Symbol:    symbol,
			Side:      side,
			Type:      engine.Limit,
			Price:     price,
			Amount:    uint64(5 + rand.Intn(25)),
			Timestamp: time.Now().UnixNano(),
		}
		if _, err := sim.eng.ProcessOrderWithWALNotify(order, sim.wal, publishOrderEvents(sim.hub, symbol)); err != nil {
			log.Printf("simulator: failed to place ladder order %d for %s: %v", order.ID, symbol, err)
		}
	}
}

// SeedSymbol pre-populates 10 bids and 10 asks around the symbol's reference mid price.
func (sim *MarketSimulator) SeedSymbol(symbol string) {
	mid, ok := referenceMid(symbol)
	if !ok {
		return
	}
	sim.placeLadder(symbol, engine.Buy, mid)
	sim.placeLadder(symbol, engine.Sell, mid)
}

// SeedMarket pre-populates realistic bids and asks for all symbols.
func (sim *MarketSimulator) SeedMarket() {
	for _, sym := range supportedSymbols {
		sim.SeedSymbol(sym)
	}
}

// EnsureLiquidity re-seeds whichever side of the book is empty so the simulator never stalls.
func (sim *MarketSimulator) EnsureLiquidity(symbol string) {
	ob, ok := sim.eng.GetOrderBook(symbol)
	if !ok {
		return
	}
	bid, hasBid, ask, hasAsk := ob.TopOfBook()
	switch {
	case !hasBid && !hasAsk:
		sim.SeedSymbol(symbol)
	case !hasAsk:
		sim.placeLadder(symbol, engine.Sell, bid)
	case !hasBid:
		sim.placeLadder(symbol, engine.Buy, ask)
	}
}

// Start begins periodic order placement. Calling it while running is a no-op.
func (sim *MarketSimulator) Start() {
	sim.mu.Lock()
	defer sim.mu.Unlock()
	sim.startLocked()
}

func (sim *MarketSimulator) startLocked() {
	if sim.running {
		return
	}
	stop, done := make(chan struct{}), make(chan struct{})
	sim.stop, sim.done, sim.running = stop, done, true
	go sim.loop(stop, done)
}

// Stop halts the simulator and waits until its goroutine has fully exited.
func (sim *MarketSimulator) Stop() {
	sim.mu.Lock()
	if !sim.running {
		sim.mu.Unlock()
		return
	}
	close(sim.stop)
	done := sim.done
	sim.running = false
	sim.mu.Unlock()
	<-done
}

// Toggle toggles the simulator on or off and returns the new running state.
func (sim *MarketSimulator) Toggle() bool {
	sim.mu.Lock()
	if sim.running {
		close(sim.stop)
		done := sim.done
		sim.running = false
		sim.mu.Unlock()
		<-done
		return false
	}
	sim.startLocked()
	sim.mu.Unlock()
	return true
}

func (sim *MarketSimulator) IsRunning() bool {
	sim.mu.Lock()
	defer sim.mu.Unlock()
	return sim.running
}

// loop only ever reads its own stop channel (passed by value), never the sim.stop field.
func (sim *MarketSimulator) loop(stop <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	ticker := time.NewTicker(600 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			sim.step()
		}
	}
}

func (sim *MarketSimulator) step() {
	symbol := supportedSymbols[rand.Intn(len(supportedSymbols))]
	ob, exists := sim.eng.GetOrderBook(symbol)
	if !exists {
		return
	}

	bestBid, hasBid, bestAsk, hasAsk := ob.TopOfBook()
	if !hasBid || !hasAsk {
		sim.EnsureLiquidity(symbol)
		return
	}

	// 70% Limit orders, 30% Market orders
	isMarket := rand.Float32() < 0.30
	side := engine.Side(rand.Intn(2))
	qty := uint64(1 + rand.Intn(8))

	var price uint64
	if side == engine.Buy {
		// Buy near best ask to trigger trade, or near best bid to add liquidity
		if isMarket || rand.Float32() < 0.4 {
			price = bestAsk
		} else {
			offset := uint64(rand.Intn(30))
			if bestBid > offset {
				price = bestBid - offset
			} else {
				price = 1 // prevents uint64 underflow
			}
		}
	} else {
		// Sell near best bid to trigger trade, or near best ask to add liquidity
		if isMarket || rand.Float32() < 0.4 {
			price = bestBid
		} else {
			price = bestAsk + uint64(rand.Intn(30))
		}
	}

	orderType := engine.Limit
	if isMarket {
		orderType = engine.Market
	}

	order := &engine.Order{
		ID:        sim.eng.NextOrderID(),
		Symbol:    symbol,
		Side:      side,
		Type:      orderType,
		Price:     price,
		Amount:    qty,
		Timestamp: time.Now().UnixNano(),
	}
	if _, err := sim.eng.ProcessOrderWithWALNotify(order, sim.wal, publishOrderEvents(sim.hub, symbol)); err != nil {
		log.Printf("simulator: order %d rejected: %v", order.ID, err)
	}
}
```

### Fix 3.3 — Simulator tests (`simulator_test.go`)

1. In `setupTestSimulator`'s `cleanup`, replace
   ```go
   if sim.IsRunning() {
       sim.Toggle()
   }
   ```
   with
   ```go
   sim.Stop() // waits for the goroutine to exit before the WAL is closed
   ```
2. In `TestSimulatorLiveOrderExecution`: change `time.After(3 * time.Second)` to `time.After(10 * time.Second)` (the old 3s window flaked ~1–2% of runs), and replace the final `sim.Toggle()` with `sim.Stop()`.
3. Add:

```go
func TestSimulatorRapidToggleIsRaceFree(t *testing.T) {
	sim, eng, _, _, cleanup := setupTestSimulator(t)
	defer cleanup()
	for _, sym := range supportedSymbols {
		eng.RegisterSymbol(sym)
	}
	sim.SeedMarket()
	for i := 0; i < 20; i++ {
		sim.Toggle()
	}
	if sim.IsRunning() {
		t.Fatal("expected simulator stopped after an even number of toggles")
	}
}

func TestSimulatorRefillsEmptySide(t *testing.T) {
	sim, eng, _, _, cleanup := setupTestSimulator(t)
	defer cleanup()
	eng.RegisterSymbol("AAPL")
	sim.placeLadder("AAPL", engine.Buy, 15000) // bids only, asks empty

	sim.EnsureLiquidity("AAPL")

	ob, _ := eng.GetOrderBook("AAPL")
	bid, hasBid, ask, hasAsk := ob.TopOfBook()
	if !hasBid || !hasAsk {
		t.Fatalf("expected both sides after EnsureLiquidity (bid=%v ask=%v)", hasBid, hasAsk)
	}
	if ask <= bid {
		t.Fatalf("refilled asks must not cross the book: bid=%d ask=%d", bid, ask)
	}
}
```

---

## PHASE 4 — Frontend (`server/public/app.js`, `server/public/index.html`)

No build step. After this phase, run the server and verify in the browser (checklist at the end of this phase).

### Fix 4.1 — Replace fabricated state (`app.js` lines 5–22)

Replace the whole `const state = { ... };` block with:

```js
const SYMBOLS = ['AAPL', 'TSLA', 'BTC-USD'];

function emptyStats() {
  return { high: null, low: null, volume: 0, tradesCount: 0, openPrice: null, lastPrice: null };
}

const state = {
  activeSymbol: 'AAPL',
  activeChartTab: 'price', // 'price' | 'depth'
  side: 0, // 0 = Buy, 1 = Sell
  type: 0, // 0 = Limit, 1 = Market
  myOrders: [],
  ws: null,
  audioEnabled: true,
  botRunning: false, // synced from /simulator/status on load
  // Session statistics, built only from trades received in this browser session.
  statsBySymbol: Object.fromEntries(SYMBOLS.map(s => [s, emptyStats()])),
  recentTrades: Object.fromEntries(SYMBOLS.map(s => [s, []])),
  // Maker fills seen over WS for orders not yet in myOrders (POST response still in flight).
  earlyMakerFills: new Map(),
  bookRequestId: 0,
  lastBook: { bids: [], asks: [] },
  hoverPriceChart: null,
  hoverDepthChart: null,
};
```

### Fix 4.2 — Candles: no fake history, time-bucketed by trade timestamp (`app.js` lines 73–164)

Replace everything from `const CANDLE_PERIOD_MS = 15000;` through the end of `updateOHLCHeader` with:

```js
const CANDLE_PERIOD_MS = 15000; // 15-second candles
const MAX_CANDLES = 80;
const candles = Object.fromEntries(SYMBOLS.map(s => [s, []]));

function updateCandleOnTrade(symbol, price, amount, tsMs) {
  const list = candles[symbol];
  if (!list) return;

  const bucket = Math.floor(tsMs / CANDLE_PERIOD_MS) * CANDLE_PERIOD_MS;
  const last = list[list.length - 1];

  if (!last || bucket > last.time) {
    // Fill quiet periods with flat candles so the x-axis stays linear in time.
    if (last) {
      const firstGap = Math.max(last.time + CANDLE_PERIOD_MS, bucket - MAX_CANDLES * CANDLE_PERIOD_MS);
      for (let t = firstGap; t < bucket; t += CANDLE_PERIOD_MS) {
        list.push({ time: t, open: last.close, high: last.close, low: last.close, close: last.close, volume: 0 });
      }
    }
    list.push({ time: bucket, open: price, high: price, low: price, close: price, volume: amount });
    while (list.length > MAX_CANDLES) list.shift();
  } else {
    const c = list.find(x => x.time === bucket);
    if (!c) return; // older than the retained window
    c.high = Math.max(c.high, price);
    c.low = Math.min(c.low, price);
    c.volume += amount;
    if (c === last) c.close = price;
  }

  if (symbol === state.activeSymbol) {
    updateOHLCHeader();
    if (state.activeChartTab === 'price') renderPriceChart();
  }
}

function updateOHLCHeader(customCandle = null) {
  const list = candles[state.activeSymbol];
  const c = customCandle || (list && list[list.length - 1]);
  if (!c) {
    [ohlcOpen, ohlcHigh, ohlcLow, ohlcClose, ohlcVol].forEach(el => { if (el) el.textContent = '—'; });
    ohlcClose.className = '';
    return;
  }

  ohlcOpen.textContent = c.open.toFixed(2);
  ohlcHigh.textContent = c.high.toFixed(2);
  ohlcLow.textContent = c.low.toFixed(2);
  ohlcClose.textContent = c.close.toFixed(2);
  ohlcClose.className = c.close >= c.open ? 'text-green' : 'text-red';

  if (ohlcVol) {
    ohlcVol.textContent = c.volume >= 1000 ? (c.volume / 1000).toFixed(1) + 'K' : c.volume.toString();
  }
}
```

### Fix 4.3 — Price chart empty state and sane candle width (`renderPriceChart`)

1. Replace
   ```js
   const list = candles[state.activeSymbol];
   if (!list || list.length === 0) return;
   ```
   with
   ```js
   const list = candles[state.activeSymbol];
   if (!list || list.length === 0) {
     ctx.fillStyle = '#475569';
     ctx.font = '11px "Plus Jakarta Sans", sans-serif';
     ctx.textAlign = 'center';
     ctx.fillText('Waiting for trades...', width / 2, height / 2);
     return;
   }
   ```
2. Fix the comment `// Add 4% padding to price bounds` → `// Add 8% padding to price bounds`.
3. Replace
   ```js
   const numCandles = list.length;
   const slotW = chartW / numCandles;
   ```
   with
   ```js
   const slotW = chartW / Math.max(list.length, 40); // fixed minimum slot count keeps candle width sane
   ```

### Fix 4.4 — Depth chart with a real, linear price axis (`app.js` lines 388–599)

Replace the whole `drawDepthChart` function with:

```js
function drawDepthChart(bids, asks) {
  state.lastBook = { bids: bids || [], asks: asks || [] };
  if (!depthCanvas || state.activeChartTab !== 'depth') return;

  const { ctx, width, height } = setupCanvasDPI(depthCanvas);
  ctx.clearRect(0, 0, width, height);

  const b = state.lastBook.bids.slice(0, 24); // best (highest) first
  const a = state.lastBook.asks.slice(0, 24); // best (lowest) first

  if (b.length === 0 && a.length === 0) {
    ctx.fillStyle = '#475569';
    ctx.font = '11px "Plus Jakarta Sans", sans-serif';
    ctx.textAlign = 'center';
    ctx.fillText('Waiting for Order Book Depth...', width / 2, height / 2);
    return;
  }

  let running = 0;
  const cumBids = b.map(l => ({ price: l.price / 100, cum: (running += l.volume) }));
  running = 0;
  const cumAsks = a.map(l => ({ price: l.price / 100, cum: (running += l.volume) }));

  // Linear price domain covering both sides.
  const prices = cumBids.concat(cumAsks).map(l => l.price);
  let lo = Math.min(...prices);
  let hi = Math.max(...prices);
  const pad = (hi - lo) * 0.05 || Math.max(hi * 0.001, 0.01);
  lo -= pad;
  hi += pad;

  const paddingBottom = 24;
  const topPad = 14;
  const drawH = height - paddingBottom;
  const maxCum = Math.max(
    cumBids.length ? cumBids[cumBids.length - 1].cum : 0,
    cumAsks.length ? cumAsks[cumAsks.length - 1].cum : 0,
    1
  );
  const xOf = p => ((p - lo) / (hi - lo)) * width;
  const yOf = c => drawH - (c / maxCum) * (drawH - topPad);
  const xToPrice = x => lo + (x / width) * (hi - lo);
  const mid = cumBids.length && cumAsks.length ? (cumBids[0].price + cumAsks[0].price) / 2 : null;

  // 1. Grid
  ctx.strokeStyle = 'rgba(255, 255, 255, 0.035)';
  ctx.setLineDash([3, 3]);
  ctx.lineWidth = 1;
  ctx.font = '10px "JetBrains Mono", monospace';
  ctx.fillStyle = '#64748b';
  for (let i = 1; i <= 4; i++) {
    const y = drawH - (i / 4) * (drawH - topPad);
    ctx.beginPath();
    ctx.moveTo(0, y);
    ctx.lineTo(width, y);
    ctx.stroke();
    ctx.textAlign = 'left';
    ctx.fillText(Math.round((i / 4) * maxCum).toLocaleString(), 8, y - 3);
  }
  ctx.setLineDash([]);

  // 2. Bids: depth at price p = total bid volume priced >= p
  if (cumBids.length) {
    ctx.beginPath();
    ctx.moveTo(xOf(cumBids[0].price), drawH);
    ctx.lineTo(xOf(cumBids[0].price), yOf(cumBids[0].cum));
    for (let i = 1; i < cumBids.length; i++) {
      const x = xOf(cumBids[i].price);
      ctx.lineTo(x, yOf(cumBids[i - 1].cum));
      ctx.lineTo(x, yOf(cumBids[i].cum));
    }
    ctx.lineTo(0, yOf(cumBids[cumBids.length - 1].cum));
    ctx.lineTo(0, drawH);
    ctx.closePath();
    const bidGrad = ctx.createLinearGradient(0, 0, 0, drawH);
    bidGrad.addColorStop(0, 'rgba(0, 240, 144, 0.28)');
    bidGrad.addColorStop(1, 'rgba(0, 240, 144, 0.01)');
    ctx.fillStyle = bidGrad;
    ctx.fill();
    ctx.strokeStyle = '#00f090';
    ctx.lineWidth = 2;
    ctx.stroke();
  }

  // 3. Asks: depth at price p = total ask volume priced <= p
  if (cumAsks.length) {
    ctx.beginPath();
    ctx.moveTo(xOf(cumAsks[0].price), drawH);
    ctx.lineTo(xOf(cumAsks[0].price), yOf(cumAsks[0].cum));
    for (let i = 1; i < cumAsks.length; i++) {
      const x = xOf(cumAsks[i].price);
      ctx.lineTo(x, yOf(cumAsks[i - 1].cum));
      ctx.lineTo(x, yOf(cumAsks[i].cum));
    }
    ctx.lineTo(width, yOf(cumAsks[cumAsks.length - 1].cum));
    ctx.lineTo(width, drawH);
    ctx.closePath();
    const askGrad = ctx.createLinearGradient(0, 0, 0, drawH);
    askGrad.addColorStop(0, 'rgba(255, 51, 88, 0.28)');
    askGrad.addColorStop(1, 'rgba(255, 51, 88, 0.01)');
    ctx.fillStyle = askGrad;
    ctx.fill();
    ctx.strokeStyle = '#ff3358';
    ctx.lineWidth = 2;
    ctx.stroke();
  }

  // 4. Mid-market line at its true price position
  if (mid !== null) {
    ctx.strokeStyle = 'rgba(255, 255, 255, 0.2)';
    ctx.setLineDash([3, 3]);
    ctx.beginPath();
    ctx.moveTo(xOf(mid), 0);
    ctx.lineTo(xOf(mid), drawH);
    ctx.stroke();
    ctx.setLineDash([]);
  }

  // 5. Price axis: edges and mid, at their real positions
  ctx.font = '10px "JetBrains Mono", monospace';
  ctx.fillStyle = '#64748b';
  ctx.textAlign = 'left';
  ctx.fillText(`$${xToPrice(0).toFixed(2)}`, 4, height - 8);
  ctx.textAlign = 'right';
  ctx.fillText(`$${xToPrice(width).toFixed(2)}`, width - 4, height - 8);
  if (mid !== null) {
    ctx.fillStyle = '#94a3b8';
    ctx.textAlign = 'center';
    ctx.fillText(`MID $${mid.toFixed(2)}`, xOf(mid), height - 8);
  }

  // 6. Hover tooltip: cumulative depth available up to the hovered price
  if (state.hoverDepthChart && state.hoverDepthChart.y <= drawH) {
    const { x, y } = state.hoverDepthChart;
    const p = xToPrice(x);
    const onBidSide = mid !== null ? p <= mid : cumBids.length > 0;
    let text = '';
    if (onBidSide) {
      const lv = cumBids.filter(l => l.price >= p);
      if (lv.length) text = `BIDS ≥ $${p.toFixed(2)} | DEPTH: ${lv[lv.length - 1].cum.toLocaleString()}`;
    } else {
      const lv = cumAsks.filter(l => l.price <= p);
      if (lv.length) text = `ASKS ≤ $${p.toFixed(2)} | DEPTH: ${lv[lv.length - 1].cum.toLocaleString()}`;
    }

    ctx.strokeStyle = 'rgba(255, 255, 255, 0.3)';
    ctx.setLineDash([2, 2]);
    ctx.beginPath();
    ctx.moveTo(x, 0);
    ctx.lineTo(x, drawH);
    ctx.stroke();
    ctx.setLineDash([]);

    if (text) {
      const txtW = ctx.measureText(text).width + 16;
      const boxX = Math.max(10, Math.min(width - txtW - 10, x - txtW / 2));
      const boxY = Math.max(10, y - 28);
      ctx.fillStyle = '#0f172a';
      ctx.strokeStyle = onBidSide ? '#00f090' : '#ff3358';
      ctx.lineWidth = 1;
      ctx.beginPath();
      ctx.roundRect(boxX, boxY, txtW, 20, 4);
      ctx.fill();
      ctx.stroke();
      ctx.fillStyle = '#f8fafc';
      ctx.textAlign = 'center';
      ctx.fillText(text, boxX + txtW / 2, boxY + 13.5);
    }
  }
}
```

### Fix 4.5 — WebSocket event handling for all symbols (`app.js` lines 695–744)

1. In `connectWebSocket`, change `state.ws.onopen` to:
   ```js
   state.ws.onopen = () => {
     wsStatus.innerHTML = '<span class="dot-pulse"></span><span class="telemetry-val">WS LIVE</span>';
     fetchOrderBook();
     syncBotStatus();
   };
   ```

2. Replace `handleServerEvent` with:

```js
function handleServerEvent(msg) {
  if (msg.type === 'trades') {
    updateOpenOrdersAfterMatch(msg.data);
    // Record every trade for its own symbol, not just the one on screen.
    msg.data.forEach(trade => recordTrade(msg.symbol, trade));
    if (msg.symbol === state.activeSymbol) {
      renderTradesStream();
      renderTickerForActiveSymbol();
      scheduleBookRefresh();
      playTradeSound();
    }
  } else if (msg.type === 'book_update') {
    if (msg.symbol === state.activeSymbol) scheduleBookRefresh();
  } else if (msg.type === 'order_cancelled') {
    state.myOrders = state.myOrders.filter(o => o.id !== msg.order_id);
    renderOpenOrders();
    if (msg.symbol === state.activeSymbol) scheduleBookRefresh();
  }
}

let bookRefreshTimer = null;
function scheduleBookRefresh() {
  if (bookRefreshTimer) return;
  bookRefreshTimer = setTimeout(() => {
    bookRefreshTimer = null;
    fetchOrderBook();
  }, 100);
}
```

### Fix 4.6 — Ignore stale order-book responses (`fetchOrderBook`)

Replace `fetchOrderBook` with:

```js
async function fetchOrderBook() {
  const symbol = state.activeSymbol;
  const requestId = ++state.bookRequestId;
  try {
    const res = await fetch(`/orderbook?symbol=${encodeURIComponent(symbol)}`);
    if (!res.ok) return;
    const data = await res.json();
    // Drop responses superseded by a newer request or a tab switch.
    if (requestId !== state.bookRequestId || data.symbol !== state.activeSymbol) return;
    renderOrderBook(data);
    drawDepthChart(data.bids || [], data.asks || []);
  } catch (err) {
    console.error('Failed to fetch order book:', err);
  }
}
```

### Fix 4.7 — Per-symbol trade recording, trade stream, ticker, and the "+-0.00%" bug (`app.js` lines 842–902)

Replace `appendTrade` and `renderTickerForActiveSymbol` with:

```js
function formatUSD(p) {
  return '$' + p.toLocaleString('en-US', { minimumFractionDigits: 2, maximumFractionDigits: 2 });
}

// Returns display text + class for a percentage. Never produces "+-0.00%".
function formatPct(pct) {
  if (pct === null || !isFinite(pct)) return { text: '—', cls: 'ticker-val' };
  const rounded = Math.round(pct * 100) / 100;
  if (rounded === 0) return { text: '0.00%', cls: 'ticker-val' };
  return {
    text: `${rounded > 0 ? '+' : ''}${rounded.toFixed(2)}%`,
    cls: `ticker-val ${rounded > 0 ? 'text-green' : 'text-red'}`,
  };
}

// Updates stats, trade history and candles for ANY symbol; DOM only for the active one.
function recordTrade(symbol, trade) {
  const stats = state.statsBySymbol[symbol];
  if (!stats) return;
  const price = trade.price / 100;

  if (stats.openPrice === null) stats.openPrice = price;
  stats.lastPrice = price;
  stats.high = stats.high === null ? price : Math.max(stats.high, price);
  stats.low = stats.low === null ? price : Math.min(stats.low, price);
  stats.volume += trade.amount;
  stats.tradesCount++;

  const list = state.recentTrades[symbol];
  list.unshift(trade);
  if (list.length > 50) list.pop();

  updateCandleOnTrade(symbol, price, trade.amount, trade.timestamp / 1e6);

  const tabPrice = document.getElementById(`tabPrice-${symbol}`);
  if (tabPrice) tabPrice.textContent = formatUSD(price);
}

function renderTradesStream() {
  const list = state.recentTrades[state.activeSymbol] || [];
  if (list.length === 0) {
    tradesStream.innerHTML = '<div class="empty-state">Waiting for executions...</div>';
    return;
  }
  tradesStream.innerHTML = list.map(t => {
    const sideClass = t.side === 0 ? 'buy' : 'sell';
    const time = new Date(t.timestamp / 1e6).toLocaleTimeString();
    return `
      <div class="trade-row">
        <span class="trade-price ${sideClass}">$${(t.price / 100).toFixed(2)}</span>
        <span>${t.amount}</span>
        <span>${time}</span>
      </div>`;
  }).join('');
}

function renderTickerForActiveSymbol() {
  const s = state.statsBySymbol[state.activeSymbol];
  const fmt = v => (v === null ? '—' : formatUSD(v));

  tickerLastPrice.textContent = fmt(s.lastPrice);
  lastTradedPrice.textContent = fmt(s.lastPrice);
  tickerHigh.textContent = fmt(s.high);
  tickerLow.textContent = fmt(s.low);
  tickerVolume.textContent = s.volume.toLocaleString();
  tickerTradesCount.textContent = s.tradesCount.toLocaleString();

  const pct = s.openPrice === null || s.lastPrice === null
    ? null
    : ((s.lastPrice - s.openPrice) / s.openPrice) * 100;
  const { text, cls } = formatPct(pct);
  tickerChange.textContent = text;
  tickerChange.className = cls;
}
```

### Fix 4.8 — Order submission uses the new response fields and handles early fills (`submitOrder`)

In `submitOrder`, replace
```js
    const data = await res.json();

    if (state.type === 0 && data.order && data.order.amount > 0) {
      state.myOrders.push(data.order);
      renderOpenOrders();
    }

    fetchOrderBook();
```
with
```js
    const data = await res.json();

    if (data.status === 'RESTING' || data.status === 'PARTIALLY_FILLED_RESTING') {
      // Subtract maker fills that arrived over WS before this response did.
      const remaining = data.remaining_amount - takeEarlyFills(data.order.id);
      if (remaining > 0) {
        state.myOrders.push({ ...data.order, amount: remaining });
        renderOpenOrders();
      }
    }

    scheduleBookRefresh();
```

### Fix 4.9 — Open orders: cancel uses the order's own symbol; 404 removes the row; early-fill tracking (`app.js` lines 961–1015)

1. In `renderOpenOrders`, change the button to:
   ```js
   <button class="btn-cancel" onclick="cancelOrder(${o.id}, '${o.symbol}')">CANCEL</button>
   ```

2. Replace `window.cancelOrder` and `updateOpenOrdersAfterMatch` with:

```js
window.cancelOrder = async function(id, symbol) {
  try {
    const res = await fetch(`/order?symbol=${encodeURIComponent(symbol)}&id=${id}`, { method: 'DELETE' });
    if (res.ok || res.status === 404) {
      // 404 means it was already filled or cancelled; either way it is no longer resting.
      state.myOrders = state.myOrders.filter(o => o.id !== id);
      renderOpenOrders();
      scheduleBookRefresh();
    } else {
      alert('Cancel failed: ' + (await res.text()));
    }
  } catch (err) {
    console.error('Cancel order error:', err);
  }
};

function updateOpenOrdersAfterMatch(trades) {
  trades.forEach(t => {
    const idx = state.myOrders.findIndex(o => o.id === t.maker_order_id);
    if (idx === -1) {
      // Could be our own order whose POST response hasn't arrived yet.
      state.earlyMakerFills.set(t.maker_order_id, (state.earlyMakerFills.get(t.maker_order_id) || 0) + t.amount);
      return;
    }
    const o = state.myOrders[idx];
    const remaining = o.amount - t.amount;
    if (remaining > 0) {
      state.myOrders[idx] = { ...o, amount: remaining };
    } else {
      state.myOrders.splice(idx, 1);
    }
  });

  // Bound memory: keep only the most recent 1000 entries (Map preserves insertion order).
  while (state.earlyMakerFills.size > 1000) {
    state.earlyMakerFills.delete(state.earlyMakerFills.keys().next().value);
  }
  renderOpenOrders();
}

function takeEarlyFills(id) {
  const filled = state.earlyMakerFills.get(id) || 0;
  state.earlyMakerFills.delete(id);
  return filled;
}
```

### Fix 4.10 — Symbol switch re-renders that symbol's trade list (`symbolTabs` click handler)

In the `symbolTabs` click handler, right after `renderTickerForActiveSymbol();` add:
```js
  renderTradesStream();
```

### Fix 4.11 — Bot button reflects the real simulator state

Replace the `btnToggleBot` click listener with:

```js
function renderBotButton() {
  btnToggleBot.classList.toggle('active', state.botRunning);
  botLabel.textContent = state.botRunning ? 'BOT: ACTIVE' : 'BOT: PAUSED';
}

async function syncBotStatus() {
  try {
    const res = await fetch('/simulator/status');
    if (!res.ok) return;
    state.botRunning = (await res.json()).running;
    renderBotButton();
  } catch (err) {
    console.error('Bot status error:', err);
  }
}

btnToggleBot.addEventListener('click', async () => {
  try {
    const res = await fetch('/simulator/toggle', { method: 'POST' });
    if (!res.ok) {
      alert('Toggle failed: ' + (await res.text()));
      return;
    }
    state.botRunning = (await res.json()).running;
    renderBotButton();
  } catch (err) {
    console.error('Toggle bot error:', err);
  }
});
```

### Fix 4.12 — Initialization (`app.js` end of file)

Replace the initialization block with:

```js
connectWebSocket();
syncBotStatus();
updateTotal();
updateOHLCHeader();
renderTickerForActiveSymbol();
renderTradesStream();
renderPriceChart();
```

### Fix 4.13 — Remove fake values and labels (`index.html`)

1. **Delete** the two hardcoded telemetry pills (lines 49–57): the `CORE: 188 ns` pill and the `WAL: ACTIVE` pill.
2. Change the `wsStatus` pill's initial contents to:
   ```html
   <span class="dot" style="background:#f5a623"></span>
   <span class="telemetry-val">CONNECTING...</span>
   ```
3. Bot button: remove the `active` class from `id="btnToggleBot"` and change `BOT: ACTIVE` to `BOT: …` (JS sets the real state).
4. Tab prices `$150.00`, `$240.00`, `$64,000.00` → `—`.
5. Ticker bar: comment `24h Ticker Statistics Bar` → `Session Statistics Bar`. Labels: `24H CHANGE` → `SESSION CHANGE`, `24H HIGH` → `SESSION HIGH`, `24H LOW` → `SESSION LOW`, `24H VOLUME` → `SESSION VOLUME`. Initial values: `tickerLastPrice`, `tickerChange`, `tickerHigh`, `tickerLow` → `—` (and remove `text-green` from `tickerChange`); `tickerVolume` → `0`.
6. OHLC bar initial values (`ohlcOpen/High/Low/Close/Vol`) → `—`, and remove `class="text-green"` from `ohlcClose`.
7. `lastTradedPrice` initial `$150.00` → `—`.

### Phase 4 browser checklist (verify manually)
- Chart shows "Waiting for trades..." at first, then real candles appear.
- Switching tabs shows that symbol's own trades, stats, candles and tab price; nothing from the previous symbol.
- Rapid bot activity never produces `WS parse error` in the console.
- Depth chart mid line sits between best bid and best ask at the right price.
- Placing a limit order that rests updates the book without waiting for a trade.
- Pausing the bot, then reloading, shows `BOT: PAUSED`.

---

## PHASE 5 — README corrections (`README.md`)

1. **Benchmarks:** after Phase 1, run
   ```bash
   cd server && go test -bench=. -benchmem -run='^$' ./engine/
   ```
   on the target machine, and replace **every** benchmark figure in the README with the new output: the table, the code block, and the "Key Engineering Highlights" bullet (`~5.3M orders/sec`, `188.5 ns/op`). Do not keep any old number.
2. **Units:** state figures as `X ns/op (1 op = 1 matching buy + 1 replenishing sell)` and, separately, `≈ X/2 ns per order`. Never label a per-order figure as "ns/op".
3. **Allocations row:** replace the explanation with: "one `Order` per submitted order, plus one `Trade` and one trades-slice allocation per match". Use the measured allocs/op count.
4. **Methodology note:** replace the benchmark description with: "Each iteration submits a buy that matches one resting ask, then replenishes one unit at the exact traded price, so the book shape is constant and every iteration exercises the matching path."
5. **Complexity claims:** in §1 change the cancel bullet to "**Cancel order:** O(log P) binary search to find the price level, then O(1) unlink; if the level empties, an extra O(P) slice shift." In §2 replace "instant O(1) lookup for order cancellation" with "O(1) order lookup by ID". In highlights replace "Architected O(1) order operations" with "O(1) queue operations within a price level and O(log P) price-level lookup".
6. **Order IDs (§2):** replace the "Unified ID Namespace & Reconciliation" text with: "Order IDs are always assigned by the server (`eng.NextOrderID()`), and requests that include an `id` are rejected with 400. After WAL recovery the generator is advanced past the highest recovered ID (`SetMinOrderID`)." Remove the phrase "verified atomically to prevent map collisions and orphaned resting orders".
7. **§3 wording:** replace the "TOCTOU-Free Order Ingestion" sentence with: "Validation, WAL append + `fsync`, and matching run inside the per-book lock. If the WAL append fails, the order is rejected with 503 and nothing is applied to the book. Trade and book events are published inside the same lock, so they reach clients in execution order."
8. **§4 statuses:** replace the status list with: `FILLED`, `RESTING` (limit), `PARTIALLY_FILLED_RESTING` (limit), `PARTIALLY_FILLED` (market), `UNFILLED` (market). Add: "`order.amount` in the response is the submitted amount; use `remaining_amount` for what is left."
9. **§5 WAL:** replace the three bullets with:
   - "**Durable append:** each placement/cancellation is written and `fsync`ed under one lock before the HTTP response. On a failed write the file is rolled back to its previous length; on a failed `fsync` the log is rolled back and disabled (all further orders get 503) until restart."
   - "**Recovery:** replays every record. An incomplete or corrupt *final* record, which was never acknowledged, is dropped. Corruption *followed by more records* aborts startup instead of truncating acknowledged data."
   - Keep the "admission-gated" bullet as is.
10. **§6 UI:** "24h Ticker Stats" → "**Session Ticker Stats:** high, low, volume and change computed from trades received since the page loaded." Candles bullet: add "built only from live trades, bucketed into 15-second intervals by trade timestamp".
11. **Mermaid diagram:** replace with
    ```mermaid
    graph TD
        Client["Trading Terminal / Bots"] -->|HTTP POST / DELETE| API["REST API"]
        Client -->|WebSocket| WSHub["WebSocket Hub"]
        API -->|Route by Symbol| Engine["Multi-Asset Engine"]
        Sim["Market Simulator Bot"] -->|Route by Symbol| Engine
        Engine -->|AAPL| OB1["AAPL OrderBook"]
        Engine -->|TSLA| OB2["TSLA OrderBook"]
        Engine -->|BTC-USD| OB3["BTC-USD OrderBook"]
        OB1 -->|Append + fsync| WAL["Write-Ahead Log (wal.log)"]
        OB2 -->|Append + fsync| WAL
        OB3 -->|Append + fsync| WAL
        OB1 -->|Trades / book updates| WSHub
        OB2 -->|Trades / book updates| WSHub
        OB3 -->|Trades / book updates| WSHub
        WSHub -->|One JSON event per frame| Client
    ```
12. **API reference:** remove `"id": 101` and `"id": 102` from both curl examples, and add a note: "`Content-Type: application/json` is required (415 otherwise). Browser requests from untrusted origins get 403." Add an error table: `400` invalid order / unknown symbol / client-supplied id, `403` origin not allowed, `404` order not found, `409` duplicate order ID, `415` wrong content type, `503` WAL unavailable.
13. **WebSocket section:** "Receives one JSON object per frame: `trades` (`data` = array of trades), `book_update` (a limit order rested; refetch `/orderbook`), and `order_cancelled`."

---

## PHASE 6 — Final gate

From `server/`:
```bash
gofmt -l .
go vet ./...
go test -race -count=3 ./...
go test -bench=. -benchmem -run='^$' ./...
```
All tests must pass three times in a row. Then run the server (`go run .`), do the Phase 4 browser checklist, stop it with Ctrl+C, and confirm the log shows a clean shutdown with no errors.
