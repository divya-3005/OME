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

func resolvePublicDir() string {
	if info, err := os.Stat("./public"); err == nil && info.IsDir() {
		return "./public"
	}
	if info, err := os.Stat("./server/public"); err == nil && info.IsDir() {
		return "./server/public"
	}
	return "./public"
}

func resolveWALPath() string {
	if p := os.Getenv("WAL_PATH"); p != "" {
		return p
	}
	if info, err := os.Stat("./server"); err == nil && info.IsDir() {
		return "server/wal.log"
	}
	return "wal.log"
}

func main() {
	eng := engine.NewEngine()

	hub := NewHub()
	go hub.Run()

	for _, sym := range supportedSymbols {
		eng.RegisterSymbol(sym)
	}

	wal, err := engine.OpenWAL(resolveWALPath())
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
	mux.HandleFunc("OPTIONS /order", handleOptions)
	mux.HandleFunc("OPTIONS /simulator/toggle", handleOptions)
	mux.HandleFunc("POST /order", requireAllowedOrigin(handlePlaceOrder(eng, hub, wal)))
	mux.HandleFunc("DELETE /order", requireAllowedOrigin(handleCancelOrder(eng, hub, wal)))
	mux.HandleFunc("GET /orderbook", handleGetOrderBook(eng))
	mux.HandleFunc("GET /trades", handleGetTrades(eng, hub))
	mux.HandleFunc("GET /orders", handleGetOrders(eng))
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
	mux.Handle("/", http.FileServer(http.Dir(resolvePublicDir())))

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	serverErr := make(chan error, 1)
	go func() { serverErr <- srv.ListenAndServe() }()
	log.Printf("Order Matching Engine running on http://localhost:%s", port)
	log.Printf("WebSocket stream available at ws://localhost:%s/ws", port)

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

	// Order matters: stop accepting requests, stop the bot, notify & close WS clients, then close the WAL.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("HTTP shutdown: %v", err)
	}
	sim.Stop()
	hub.Stop()
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
		if origin := r.Header.Get("Origin"); origin != "" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
		}
		next(w, r)
	}
}

// handleOptions responds to CORS preflight requests from allowed origins.
func handleOptions(w http.ResponseWriter, r *http.Request) {
	if !isAllowedOrigin(r) {
		http.Error(w, "origin not allowed", http.StatusForbidden)
		return
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Access-Control-Allow-Methods", "POST, DELETE, GET, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	}
	w.WriteHeader(http.StatusNoContent)
}

func isJSONRequest(r *http.Request) bool {
	mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	return err == nil && mt == "application/json"
}

// writeEngineError maps engine errors to HTTP status codes.
func writeEngineError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, engine.ErrInvalidOrder):
		status = http.StatusBadRequest
	case errors.Is(err, engine.ErrUnknownSymbol):
		status = http.StatusNotFound
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
			hub.RecordTrades(symbol, trades)
			hub.BroadcastJSON(map[string]interface{}{
				"type":   "trades",
				"symbol": symbol,
				"data":   trades,
			})
		}
		if len(trades) > 0 || rested {
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
		select {
		case hub.register <- client:
		case <-hub.stop:
			conn.Close()
			return
		}
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
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 1MB max payload

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
		order.Timestamp = time.Now().UnixMilli()

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

		if symbol == "" || idStr == "" {
			http.Error(w, "query params 'symbol' and 'id' are required", http.StatusBadRequest)
			return
		}

		orderID, err := strconv.ParseUint(idStr, 10, 64)
		if err != nil || orderID == 0 {
			http.Error(w, "query param 'id' must be a positive integer", http.StatusBadRequest)
			return
		}

		success, err := eng.CancelOrderWithWALNotify(symbol, orderID, wal, func() {
			hub.BroadcastJSON(map[string]interface{}{
				"type":     "order_cancelled",
				"symbol":   symbol,
				"order_id": orderID,
			})
			hub.BroadcastJSON(map[string]interface{}{
				"type":   "book_update",
				"symbol": symbol,
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
		if symbol == "" {
			http.Error(w, "query param 'symbol' is required", http.StatusBadRequest)
			return
		}
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

// handleGetTrades returns recent trade executions for GET /trades?symbol=AAPL
func handleGetTrades(eng *engine.Engine, hub *Hub) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		symbol := r.URL.Query().Get("symbol")
		if symbol == "" {
			http.Error(w, "query param 'symbol' is required", http.StatusBadRequest)
			return
		}
		if _, exists := eng.GetOrderBook(symbol); !exists {
			http.Error(w, "symbol not found", http.StatusNotFound)
			return
		}
		trades := hub.GetRecentTrades(symbol)
		if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
			if n, err := strconv.Atoi(limitStr); err == nil && n > 0 && n < len(trades) {
				trades = trades[len(trades)-n:]
			}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"symbol": symbol,
			"trades": trades,
		})
	}
}

// handleGetOrders returns currently resting orders for GET /orders?symbol=AAPL
func handleGetOrders(eng *engine.Engine) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		symbol := r.URL.Query().Get("symbol")
		if symbol == "" {
			http.Error(w, "query param 'symbol' is required", http.StatusBadRequest)
			return
		}
		orders, exists := eng.GetOpenOrders(symbol)
		if !exists {
			http.Error(w, "symbol not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"symbol": symbol,
			"orders": orders,
		})
	}
}
