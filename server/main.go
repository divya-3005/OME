package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/divya-3005/OME/server/engine"
)

func main() {
	// Initialize the engine and websocket hub
	eng := engine.NewEngine()
	eng.SetMinOrderID(uint64(time.Now().UnixMilli()))

	hub := NewHub()
	go hub.Run()

	// Pre-register supported symbols
	eng.RegisterSymbol("AAPL")
	eng.RegisterSymbol("TSLA")
	eng.RegisterSymbol("BTC-USD")

	// Initialize Write-Ahead Log (WAL) and recover past state
	wal, err := engine.OpenWAL("wal.log")
	if err != nil {
		log.Fatalf("failed to open WAL: %v", err)
	}
	defer wal.Close()

	if maxID, err := wal.Recover(eng); err != nil {
		log.Printf("WAL recovery warning: %v", err)
	} else {
		log.Printf("WAL recovery complete: restored state (highest order ID: %d)", maxID)
	}

	// Initialize Market Simulator & Seeder
	sim := NewMarketSimulator(eng, hub, wal)

	// Seed each symbol individually if its order book is empty
	symbols := []string{"AAPL", "TSLA", "BTC-USD"}
	for _, sym := range symbols {
		if ob, exists := eng.GetOrderBook(sym); exists {
			bids, _ := ob.GetSnapshot()
			if len(bids) == 0 {
				log.Printf("Seeding market with initial liquidity for %s...", sym)
				sim.SeedSymbol(sym)
			}
		}
	}

	// Start live background bot simulation
	sim.Start()
	log.Println("Market Simulator active: simulating live institutional order flow")

	// REST & WebSocket endpoints
	http.HandleFunc("POST /order", handlePlaceOrder(eng, hub, wal))
	http.HandleFunc("DELETE /order", handleCancelOrder(eng, hub, wal))
	http.HandleFunc("GET /orderbook", handleGetOrderBook(eng))
	http.HandleFunc("/ws", handleWebSocket(hub))

	// Simulator control endpoints
	http.HandleFunc("POST /simulator/toggle", func(w http.ResponseWriter, r *http.Request) {
		running := sim.Toggle()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]bool{"running": running})
	})
	http.HandleFunc("GET /simulator/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]bool{"running": sim.IsRunning()})
	})

	// Serve static frontend UI
	http.Handle("/", http.FileServer(http.Dir("./public")))

	log.Println("Order Matching Engine running on http://localhost:8080")
	log.Println("WebSocket stream available at ws://localhost:8080/ws")
	log.Fatal(http.ListenAndServe(":8080", nil))
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

// handlePlaceOrder processes incoming POST /order requests with atomic WAL logging & fsync (eliminates TOCTOU)
func handlePlaceOrder(eng *engine.Engine, hub *Hub, wal *engine.WAL) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var order engine.Order
		if err := json.NewDecoder(r.Body).Decode(&order); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}

		// Auto-generate unique order ID if omitted or 0
		if order.ID == 0 {
			order.ID = eng.NextOrderID()
		}

		if order.Timestamp == 0 {
			order.Timestamp = time.Now().UnixNano()
		}

		// Validate Side and Type enum integrity
		if order.Side != engine.Buy && order.Side != engine.Sell {
			http.Error(w, fmt.Sprintf("invalid order side: %d (must be 0 for Buy or 1 for Sell)", order.Side), http.StatusBadRequest)
			return
		}
		if order.Type != engine.Limit && order.Type != engine.Market {
			http.Error(w, fmt.Sprintf("invalid order type: %d (must be 0 for Limit or 1 for Market)", order.Type), http.StatusBadRequest)
			return
		}

		requestedAmount := order.Amount
		trades, err := eng.ProcessOrderWithWAL(&order, wal)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		var filledAmount uint64 = 0
		for _, t := range trades {
			filledAmount += t.Amount
		}
		remainingAmount := requestedAmount - filledAmount

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

		if len(trades) > 0 {
			hub.BroadcastJSON(map[string]interface{}{
				"type":   "trades",
				"symbol": order.Symbol,
				"data":   trades,
			})
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"order":            order,
			"trades":           trades,
			"requested_amount": requestedAmount,
			"filled_amount":    filledAmount,
			"remaining_amount": remainingAmount,
			"status":           status,
		})
	}
}

// handleCancelOrder processes DELETE /order?symbol=AAPL&id=1 with atomic WAL logging & fsync
func handleCancelOrder(eng *engine.Engine, hub *Hub, wal *engine.WAL) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		symbol := r.URL.Query().Get("symbol")
		idStr := r.URL.Query().Get("id")

		orderID, err := strconv.ParseUint(idStr, 10, 64)
		if err != nil || symbol == "" {
			http.Error(w, "query params 'symbol' and 'id' are required", http.StatusBadRequest)
			return
		}

		// CancelOrderWithWAL atomically checks order existence, writes to WAL, calls wal.Sync(), and removes within book lock
		success, err := eng.CancelOrderWithWAL(symbol, orderID, wal)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}

		if success {
			hub.BroadcastJSON(map[string]interface{}{
				"type":     "order_cancelled",
				"symbol":   symbol,
				"order_id": orderID,
			})
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
