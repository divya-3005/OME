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
	// Initialize the engine
	eng := engine.NewEngine()

	// Pre-register supported symbols
	eng.RegisterSymbol("AAPL")
	eng.RegisterSymbol("TSLA")
	eng.RegisterSymbol("BTC-USD")

	// Set up REST endpoints (using Go 1.22+ pattern routing)
	http.HandleFunc("POST /order", handlePlaceOrder(eng))
	http.HandleFunc("DELETE /order", handleCancelOrder(eng))
	http.HandleFunc("GET /orderbook", handleGetOrderBook(eng))

	log.Println("Order Matching Engine running on http://localhost:8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}

// handlePlaceOrder processes incoming POST /order requests
func handlePlaceOrder(eng *engine.Engine) http.HandlerFunc {
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

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"order":  order,
			"trades": trades,
		})
	}
}

// handleCancelOrder processes DELETE /order?symbol=AAPL&id=1
func handleCancelOrder(eng *engine.Engine) http.HandlerFunc {
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


