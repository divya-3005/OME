package main

import (
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/divya-3005/OME/server/engine"
)


func main() {
	// Initialize the engine and websocket hub
	eng := engine.NewEngine()
	hub := NewHub()
	go hub.Run()

	// Pre-register supported symbols
	eng.RegisterSymbol("AAPL")
	eng.RegisterSymbol("TSLA")
	eng.RegisterSymbol("BTC-USD")

	// REST & WebSocket endpoints
	http.HandleFunc("POST /order", handlePlaceOrder(eng, hub))
	http.HandleFunc("DELETE /order", handleCancelOrder(eng, hub))
	http.HandleFunc("GET /orderbook", handleGetOrderBook(eng))
	http.HandleFunc("/ws", handleWebSocket(hub))

	log.Println("Order Matching Engine running on http://localhost:8080")
	log.Println("WebSocket stream available at ws://localhost:8080/ws")
	log.Fatal(http.ListenAndServe(":8080", nil))
}

// handleWebSocket upgrades incoming HTTP connections to WebSocket
func handleWebSocket(hub *Hub) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Printf("failed to upgrade websocket: %v", err)
			return
		}

		hub.register <- conn

		// Keep connection alive until client disconnects
		go func() {
			defer func() {
				hub.unregister <- conn
			}()
			for {
				if _, _, err := conn.ReadMessage(); err != nil {
					break
				}
			}
		}()
	}
}

// handlePlaceOrder processes incoming POST /order requests and broadcasts trades
func handlePlaceOrder(eng *engine.Engine, hub *Hub) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var order engine.Order
		if err := json.NewDecoder(r.Body).Decode(&order); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}

		if order.Timestamp == 0 {
			order.Timestamp = time.Now().UnixNano()
		}

		trades, err := eng.ProcessOrder(&order)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		// Broadcast executed trades via WebSocket
		if len(trades) > 0 {
			hub.BroadcastJSON(map[string]interface{}{
				"type":   "trades",
				"symbol": order.Symbol,
				"data":   trades,
			})
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"order":  order,
			"trades": trades,
		})
	}
}

// handleCancelOrder processes DELETE /order?symbol=AAPL&id=1
func handleCancelOrder(eng *engine.Engine, hub *Hub) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		symbol := r.URL.Query().Get("symbol")
		idStr := r.URL.Query().Get("id")

		orderID, err := strconv.ParseUint(idStr, 10, 64)
		if err != nil || symbol == "" {
			http.Error(w, "query params 'symbol' and 'id' are required", http.StatusBadRequest)
			return
		}

		success, err := eng.CancelOrder(symbol, orderID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
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
	type LevelSummary struct {
		Price  uint64 `json:"price"`
		Volume uint64 `json:"volume"`
	}

	return func(w http.ResponseWriter, r *http.Request) {
		symbol := r.URL.Query().Get("symbol")
		ob, exists := eng.GetOrderBook(symbol)
		if !exists {
			http.Error(w, "symbol not found", http.StatusNotFound)
			return
		}

		bids := make([]LevelSummary, 0, len(ob.Bids))
		for _, b := range ob.Bids {
			bids = append(bids, LevelSummary{Price: b.Price, Volume: b.TotalVolume})
		}

		asks := make([]LevelSummary, 0, len(ob.Asks))
		for _, a := range ob.Asks {
			asks = append(asks, LevelSummary{Price: a.Price, Volume: a.TotalVolume})
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"symbol": symbol,
			"bids":   bids,
			"asks":   asks,
		})
	}
}
