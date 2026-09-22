# High-Performance Order Matching Engine (OME) in Go

A low-latency, multi-asset financial order matching engine built from scratch in Go. Features Price-Time Priority (FIFO) matching, sub-microsecond execution, Limit & Market orders, Write-Ahead Logging (WAL) for crash recovery, an HTTP REST API, and real-time WebSocket market data streaming.

---

## ⚡ Performance Benchmarks

Benchmarked on Apple M3 (8-core ARM64) using Go 1.26:

| Metric | Benchmark Result |
| :--- | :--- |
| **Throughput** | **~5.3 Million orders / sec** |
| **Execution Latency** | **188.5 ns / operation** (sub-microsecond) |
| **Memory Allocation** | **154 B / op** |
| **GC Overhead** | **1 allocation / op** |

```bash
goos: darwin
goarch: arm64
pkg: github.com/divya-3005/OME/server/engine
cpu: Apple M3
BenchmarkProcessOrder-8   8112890   188.5 ns/op   154 B/op   1 allocs/op
PASS
```

---

## 🏗️ Architecture & Key Features

```mermaid
graph TD
    Client[Trading Clients / Bots] -->|HTTP POST / DELETE| API[REST API]
    Client -->|WebSocket| WSHub[WebSocket Hub]
    API -->|Persist Event| WAL[Write-Ahead Log (wal.log)]
    API -->|Route by Symbol| Engine[Engine Manager]
    Engine -->|AAPL| OB1[AAPL OrderBook]
    Engine -->|TSLA| OB2[TSLA OrderBook]
    Engine -->|BTC-USD| OB3[BTC-USD OrderBook]
    OB1 -->|Executed Trades| WSHub
    OB2 -->|Executed Trades| WSHub
    OB3 -->|Executed Trades| WSHub
    WSHub -->|Real-Time Broadcast| Client
```

### 1. $O(1)$ Price-Time Priority via Doubly Linked Lists
- Orders at the same price level form a **Doubly Linked List** (FIFO queue).
- **Adding an order**: Appended to the tail in **$O(1)$**.
- **Matching an order**: Pulled from the head in **$O(1)$**.
- **Cancelling an order**: Unlinked directly in **$O(1)$** without shifting elements.

### 2. $O(1)$ Order Cancellations via Hash Map
- An internal `Orders map[uint64]*Order` enables instant $O(1)$ lookup for order cancellation by ID.

### 3. Limit & Market Orders
- **Limit Orders**: Matches at or better than specified price; unfilled volume rests on the book.
- **Market Orders**: Sweeps available liquidity immediately across multiple price levels without resting.

### 4. Durability via Write-Ahead Logging (WAL)
- Every order placement and cancellation is appended to an on-disk log (`wal.log`).
- On server startup/restart, the WAL automatically replays all transactions to restore the exact in-memory state.

### 5. Multi-Asset & Real-Time Streaming
- Manages independent concurrent order books (`AAPL`, `TSLA`, `BTC-USD`).
- Real-time WebSocket hub fans out executed trades and order book updates to subscribers.

### 6. Integer Arithmetic for Zero Precision Loss
- Eliminates floating-point rounding errors by representing all prices and amounts in integer ticks/cents (`uint64`).

---

## 🚀 Getting Started

### 1. Run the Server
```bash
cd server
go run main.go hub.go
```
The server starts on `http://localhost:8080` and automatically recovers state from `wal.log` if it exists.

### 2. Run Tests & Benchmarks
```bash
cd server
# Run all unit tests (including FIFO, Market Orders, and WAL recovery)
go test -v ./...

# Run performance benchmarks with memory statistics
go test -bench=. -benchmem ./...
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

### 4. WebSocket Stream
Connect to `ws://localhost:8080/ws` to receive real-time JSON events on trades and cancellations.

---

## 💼 Resume Bullet Points (Ready to Copy)

- **Engineered a high-throughput Order Matching Engine in Go**, processing **~5.3M orders/sec** with sub-microsecond latency (**188 ns/op**) using Price-Time Priority (FIFO) matching.
- **Architected $O(1)$ order operations** using intrusive Doubly Linked Lists for price-level queues and an indexed hash map for instant order cancellations.
- **Implemented Limit and Market order execution**, supporting multi-level liquidity sweeps and zero-loss fixed-point integer pricing.
- **Built Write-Ahead Logging (WAL) for fault tolerance**, persisting order transitions to disk and enabling automated state reconstruction on crash/restart.
- **Developed a real-time WebSocket market data feed** and REST API to stream trade executions and order book updates with minimal GC overhead (1 alloc/op).
