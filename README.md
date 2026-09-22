# High-Performance Order Matching Engine (OME) & Trading Terminal

[![CI](https://github.com/divya-3005/OME/actions/workflows/ci.yml/badge.svg)](https://github.com/divya-3005/OME/actions/workflows/ci.yml)

A low-latency, multi-asset financial order matching engine and institutional trading terminal built from scratch in Go. Features sub-microsecond Price-Time Priority (FIFO) matching, Limit & Market order execution, Write-Ahead Logging (WAL) for fault-tolerant crash recovery, real-time WebSocket market data streaming, and an interactive dark-mode trading workstation with Japanese Candlestick and Step-Staircase Market Depth charts.

---

## ⚡ Performance & Benchmarks

The benchmark measures the isolated in-memory matching algorithm (`ProcessOrder`) under a single thread on an Apple M3 (8-core ARM64) using Go 1.22+:

| Metric | In-Memory Engine Benchmark | Details |
| :--- | :--- | :--- |
| **Engine-Level Throughput** | **~5.3 Million orders / sec** | In-memory matching loop |
| **Mean Execution Latency** | **188.5 ns / operation** | Sub-microsecond core matching |
| **Memory Allocation** | **154 B / op** | Intrusive DLL avoids separate node allocations |
| **GC Overhead** | **1 allocation / op** | Single heap allocation per order lifecycle |

```bash
goos: darwin
goarch: arm64
pkg: github.com/divya-3005/OME/server/engine
cpu: Apple M3
BenchmarkProcessOrder-8   8112890   188.5 ns/op   154 B/op   1 allocs/op
PASS
```

> **Note on Benchmark Methodology**: The 188.5 ns/op figure reflects the mean latency of a single `ProcessOrder` execution in memory. It is not an end-to-end figure through the HTTP layer, JSON deserialization, disk WAL fsync, and WebSocket fan-out, where throughput is bounded by network and disk I/O rather than the matching algorithm itself.

---

## 🏗️ Architecture & Key Features

```mermaid
graph TD
    Client["Trading Terminal / Bots"] -->|HTTP POST / DELETE| API["REST API"]
    Client -->|WebSocket| WSHub["WebSocket Hub"]
    Sim["Market Simulator Bot"] -->|Automated Liquidity| Engine["Multi-Asset Engine"]
    API -->|Persist Event| WAL["Write-Ahead Log (wal.log)"]
    API -->|Route by Symbol| Engine
    Engine -->|AAPL| OB1["AAPL OrderBook"]
    Engine -->|TSLA| OB2["TSLA OrderBook"]
    Engine -->|BTC-USD| OB3["BTC-USD OrderBook"]
    OB1 -->|Executed Trades| WSHub
    OB2 -->|Executed Trades| WSHub
    OB3 -->|Executed Trades| WSHub
    WSHub -->|Sub-ms Real-Time Broadcast| Client
```

### 1. Sorted Price Levels with $O(\log P)$ Search & $O(1)$ FIFO Queues
- **Price Levels**: Maintained in sorted order (bids descending, asks ascending) using binary search lookup ($O(\log P)$) with slice insertion shift ($O(P)$).
- **Intrusive Doubly Linked Lists**: Orders at each price level form an intrusive FIFO queue:
  - **Add to queue**: Appended to tail in **$O(1)$**.
  - **Pop match**: Extracted from head in **$O(1)$**.
  - **Cancel order**: Unlinked directly in **$O(1)$** without array shifting or linear scans.
- **Garbage-Collector Safe**: Pointer references are explicitly zeroed during level eviction, preventing backing-array memory retention.

### 2. $O(1)$ Order Cancellations & Unified ID Namespace
- **Instant Cancellations**: An internal `Orders map[uint64]*Order` enables instant $O(1)$ lookup for order cancellation by ID (eliminating the $O(P \times L)$ scan found in naive matching engines).
- **Unified ID Namespace & Reconciliation**: Monotonic order ID generation is managed centrally by the engine (`eng.NextOrderID()`), reconciled post-WAL recovery (`SetMinOrderID`), and verified atomically to prevent map collisions and orphaned resting orders.

### 3. Fine-Grained Concurrency & Non-Blocking Hub
- **Per-Symbol Synchronization**: Each `OrderBook` is protected by its own `sync.RWMutex`. This eliminates cross-symbol lock contention, allowing concurrent matching across distinct asset pairs (`AAPL`, `TSLA`, `BTC-USD`).
- **TOCTOU-Free Order Ingestion**: Admission validation, WAL persistence, disk `fsync`, and in-memory matching execute atomically within the book lock, eliminating time-of-check-to-time-of-use races.
- **Non-Blocking WebSocket Hub**: Implements dedicated per-client buffered channels (`send chan []byte`), write deadlines, origin verification (safeguarding against cross-site hijacking), and background `writePump` routines. A slow or wedged client cannot stall the broadcast event loop or block other traders.

### 4. Limit & Market Orders with Explicit Execution Status
- **Limit Orders**: Matches at or better than limit price; remaining volume rests on the book.
- **Market Orders**: Sweeps available liquidity immediately across multiple price levels without resting.
- **Transparent Execution Feedback**: API responses explicitly return `requested_amount`, `filled_amount`, `remaining_amount`, and execution status (`FILLED`, `PARTIALLY_FILLED`, `RESTING`, `UNFILLED`).

### 5. Durability via Write-Ahead Logging (WAL) & fsync
- **Admission-Gated Logging**: Only pre-validated, admissible orders are logged to disk (`wal.log`), adhering to strict WAL discipline (unregistered symbols or duplicate payloads are rejected before dirtying the log).
- **Physical Disk Durability**: Every committed placement and cancellation executes `wal.Sync()` (`fsync`), ensuring physical disk persistence against OS kernel crashes or sudden power loss before returning HTTP 200 OK.
- **Self-Healing Crash Recovery**: On startup, `wal.Recover()` replays all valid historical events and automatically detects and truncates corrupt/partial trailing writes left by mid-write crashes, guaranteeing future writes remain durable and uncorrupted.

### 6. Institutional Trading Terminal (Web UI)
- **Live L2 Order Book**: Real-time Bids (Green) and Asks (Red) with dynamic depth bars.
- **Japanese Candlestick Chart**: Real-time OHLC candles with high/low wicks, volume histogram sub-plot, and interactive crosshair.
- **Step-Staircase Market Depth Chart**: True step curves (`_|-|_`) visualizing cumulative liquidity slopes with hover tooltips.
- **Market Simulator Bot**: Automated background bot injecting realistic liquidity and trades with one-click pause/resume.
- **24h Ticker Stats**: Real-time High, Low, Volume, and Change % tracking.
- **Click-to-Trade**: Clicking any price in the order book immediately populates the order entry ticket.
- **Synthesized Audio Chime**: Subtle audio feedback on executions using the browser's Web Audio API.

### 7. Zero Precision Loss via Fixed-Point Integer Arithmetic
- Eliminates floating-point rounding errors by representing all prices and amounts in integer ticks/cents (`uint64`).

---

## 🎯 Key Engineering Highlights

- **Engineered a high-throughput in-memory Order Matching Engine in Go**, achieving an engine-level throughput of **~5.3M orders/sec** with a mean matching latency of **188.5 ns/op** using Price-Time Priority (FIFO).
- **Architected $O(1)$ order operations** utilizing intrusive Doubly Linked Lists for price-level order queues, binary search ($O(\log P)$) for sorted price levels, and an indexed hash map for instant order cancellations.
- **Implemented fine-grained concurrency**, isolating mutex locks per order book to enable parallel matching across asset books and race-free coordination between HTTP endpoints, WebSocket broadcasts, and background simulation bots (verified via Go's `-race` detector).
- **Implemented Limit and Market order execution**, supporting multi-level liquidity sweeps and zero-loss fixed-point integer pricing (`uint64`).
- **Built Write-Ahead Logging (WAL) for durability**, persisting order transitions to disk and enabling deterministic crash recovery on startup.
- **Developed a real-time WebSocket market data feed and institutional trading workstation** featuring an L2 order book, Japanese Candlestick chart, step-staircase liquidity depth chart, and background market maker bot.

---

## 🚀 Getting Started

### 1. Run the Server & Trading Terminal
```bash
cd server
go run .
```
The server starts on `http://localhost:8080`.
Open **[http://localhost:8080](http://localhost:8080)** in your browser to interact with the live trading terminal.

### 2. Run Tests & Benchmarks
```bash
cd server

# Run all unit tests (FIFO, Partial Fills, Market Orders, WAL Recovery)
go test -v ./...

# Run performance benchmarks with memory statistics
go test -bench=. -benchmem -run=^$ ./...
```

---

## 📡 API Reference

### 1. Place an Order
`POST /order`
```bash
# Place a Limit Buy order for 10 AAPL @ $150.00
curl -X POST http://localhost:8080/order \
  -H "Content-Type: application/json" \
  -d '{
    "id": 101,
    "symbol": "AAPL",
    "side": 0,
    "type": 0,
    "price": 15000,
    "amount": 10
  }'

# Place a Market Buy order for 5 AAPL
curl -X POST http://localhost:8080/order \
  -H "Content-Type: application/json" \
  -d '{
    "id": 102,
    "symbol": "AAPL",
    "side": 0,
    "type": 1,
    "amount": 5
  }'
```
*(Notes: `side: 0` = Buy, `side: 1` = Sell. `type: 0` = Limit, `type: 1` = Market. Price is in cents).*

### 2. View Order Book Depth
`GET /orderbook?symbol=AAPL`
```bash
curl "http://localhost:8080/orderbook?symbol=AAPL"
```

### 3. Cancel an Order
`DELETE /order?symbol=AAPL&id=101`
```bash
curl -X DELETE "http://localhost:8080/order?symbol=AAPL&id=101"
```

### 4. Real-Time WebSocket Stream
Connect to `ws://localhost:8080/ws` to receive live JSON events on trades and cancellations.
