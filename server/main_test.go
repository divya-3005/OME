package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/divya-3005/OME/server/engine"
)

func setupTestServer(t *testing.T) (*engine.Engine, *Hub, *engine.WAL, func()) {
	t.Helper()
	eng := engine.NewEngine()
	hub := NewHub()
	go hub.Run()

	tempDir := t.TempDir()
	walPath := filepath.Join(tempDir, "main_test_wal.log")
	wal, err := engine.OpenWAL(walPath)
	if err != nil {
		t.Fatalf("failed to open test WAL: %v", err)
	}

	eng.RegisterSymbol("AAPL")
	eng.RegisterSymbol("TSLA")

	cleanup := func() {
		hub.Stop()
		wal.Close()
	}

	return eng, hub, wal, cleanup
}

func TestHandlePlaceOrder(t *testing.T) {
	eng, hub, wal, cleanup := setupTestServer(t)
	defer cleanup()

	handler := handlePlaceOrder(eng, hub, wal)

	// 1. Valid resting Limit Buy order
	orderPayload := map[string]interface{}{
		"symbol": "AAPL",
		"side":   0, // Buy
		"type":   0, // Limit
		"price":  15000,
		"amount": 10,
	}
	body, _ := json.Marshal(orderPayload)
	req := httptest.NewRequest("POST", "/order", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response JSON: %v", err)
	}

	if resp["status"] != "RESTING" {
		t.Errorf("expected status RESTING, got %v", resp["status"])
	}

	orderData := resp["order"].(map[string]interface{})
	if orderData["id"] == nil || orderData["id"].(float64) == 0 {
		t.Errorf("expected server-generated order ID, got %v", orderData["id"])
	}

	// 2. Matching Limit Sell order (triggers fill)
	sellPayload := map[string]interface{}{
		"symbol": "AAPL",
		"side":   1, // Sell
		"type":   0, // Limit
		"price":  15000,
		"amount": 10,
	}
	body, _ = json.Marshal(sellPayload)
	req = httptest.NewRequest("POST", "/order", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()

	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for sell order, got %d: %s", rec.Code, rec.Body.String())
	}

	var sellResp map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &sellResp); err != nil {
		t.Fatalf("failed to parse response JSON: %v", err)
	}

	if sellResp["status"] != "FILLED" {
		t.Errorf("expected status FILLED, got %v", sellResp["status"])
	}
	trades := sellResp["trades"].([]interface{})
	if len(trades) != 1 {
		t.Errorf("expected 1 trade, got %d", len(trades))
	}

	// 3. Invalid JSON payload
	req = httptest.NewRequest("POST", "/order", bytes.NewReader([]byte("{invalid-json")))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	handler(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for bad JSON, got %d", rec.Code)
	}

	// 4. Invalid Side enum
	badSidePayload := map[string]interface{}{
		"symbol": "AAPL",
		"side":   99,
		"type":   0,
		"price":  15000,
		"amount": 10,
	}
	body, _ = json.Marshal(badSidePayload)
	req = httptest.NewRequest("POST", "/order", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	handler(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for bad side, got %d", rec.Code)
	}

	// 5. Invalid Type enum
	badTypePayload := map[string]interface{}{
		"symbol": "AAPL",
		"side":   0,
		"type":   99,
		"price":  15000,
		"amount": 10,
	}
	body, _ = json.Marshal(badTypePayload)
	req = httptest.NewRequest("POST", "/order", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	handler(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for bad type, got %d", rec.Code)
	}

	// 6. Unsupported symbol
	badSymPayload := map[string]interface{}{
		"symbol": "NON_EXISTENT",
		"side":   0,
		"type":   0,
		"price":  15000,
		"amount": 10,
	}
	body, _ = json.Marshal(badSymPayload)
	req = httptest.NewRequest("POST", "/order", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	handler(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404 for unknown symbol, got %d", rec.Code)
	}
}

func TestHandleMarketOrderVariations(t *testing.T) {
	eng, hub, wal, cleanup := setupTestServer(t)
	defer cleanup()

	handler := handlePlaceOrder(eng, hub, wal)

	// Market order on empty book -> status UNFILLED
	mktPayload := map[string]interface{}{
		"symbol": "AAPL",
		"side":   0, // Buy
		"type":   1, // Market
		"amount": 5,
	}
	body, _ := json.Marshal(mktPayload)
	req := httptest.NewRequest("POST", "/order", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]interface{}
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp["status"] != "UNFILLED" {
		t.Errorf("expected UNFILLED status on empty book, got %v", resp["status"])
	}

	// Seed resting ask of 5 shares
	limitAsk := map[string]interface{}{
		"symbol": "AAPL",
		"side":   1,
		"type":   0,
		"price":  15000,
		"amount": 5,
	}
	body, _ = json.Marshal(limitAsk)
	req = httptest.NewRequest("POST", "/order", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	handler(rec, req)

	// Market buy for 10 shares -> partially filled (5 filled, 5 unfilled because market orders do not rest)
	mktBuyLarge := map[string]interface{}{
		"symbol": "AAPL",
		"side":   0,
		"type":   1,
		"amount": 10,
	}
	body, _ = json.Marshal(mktBuyLarge)
	req = httptest.NewRequest("POST", "/order", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", rec.Code, rec.Body.String())
	}
	var largeResp map[string]interface{}
	json.Unmarshal(rec.Body.Bytes(), &largeResp)
	if largeResp["status"] != "PARTIALLY_FILLED" {
		t.Errorf("expected PARTIALLY_FILLED for market sweep, got %v", largeResp["status"])
	}
}

func TestHandleCancelOrder(t *testing.T) {
	eng, hub, wal, cleanup := setupTestServer(t)
	defer cleanup()

	placeHandler := handlePlaceOrder(eng, hub, wal)
	cancelHandler := handleCancelOrder(eng, hub, wal)

	// Place an order to cancel
	orderPayload := map[string]interface{}{
		"symbol": "AAPL",
		"side":   0,
		"type":   0,
		"price":  15000,
		"amount": 10,
	}
	body, _ := json.Marshal(orderPayload)
	req := httptest.NewRequest("POST", "/order", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	placeHandler(rec, req)

	var placeResp map[string]interface{}
	json.Unmarshal(rec.Body.Bytes(), &placeResp)
	orderID := uint64(placeResp["order"].(map[string]interface{})["id"].(float64))

	// 1. Missing query params
	req = httptest.NewRequest("DELETE", "/order", nil)
	rec = httptest.NewRecorder()
	cancelHandler(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for missing query params, got %d", rec.Code)
	}

	// 2. Non-existent order
	req = httptest.NewRequest("DELETE", "/order?symbol=AAPL&id=999999", nil)
	rec = httptest.NewRecorder()
	cancelHandler(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404 for non-existent order, got %d", rec.Code)
	}

	// 3. Successful cancellation
	cancelURL := fmt.Sprintf("/order?symbol=AAPL&id=%d", orderID)
	req = httptest.NewRequest("DELETE", cancelURL, nil)
	rec = httptest.NewRecorder()
	cancelHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for successful cancel, got %d: %s", rec.Code, rec.Body.String())
	}
	var cancelResp map[string]interface{}
	json.Unmarshal(rec.Body.Bytes(), &cancelResp)
	if cancelResp["success"] != true {
		t.Errorf("expected success: true, got %v", cancelResp["success"])
	}
}

func TestHandleGetOrderBook(t *testing.T) {
	eng, hub, wal, cleanup := setupTestServer(t)
	defer cleanup()

	bookHandler := handleGetOrderBook(eng)

	// 1. Non-existent symbol -> 404
	req := httptest.NewRequest("GET", "/orderbook?symbol=UNKNOWN", nil)
	rec := httptest.NewRecorder()
	bookHandler(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404 for unknown symbol, got %d", rec.Code)
	}

	// 2. Existing symbol with an order
	placeHandler := handlePlaceOrder(eng, hub, wal)
	orderPayload := map[string]interface{}{
		"symbol": "AAPL",
		"side":   0,
		"type":   0,
		"price":  14900,
		"amount": 25,
	}
	body, _ := json.Marshal(orderPayload)
	req = httptest.NewRequest("POST", "/order", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	placeHandler(rec, req)

	req = httptest.NewRequest("GET", "/orderbook?symbol=AAPL", nil)
	rec = httptest.NewRecorder()
	bookHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for orderbook snapshot, got %d: %s", rec.Code, rec.Body.String())
	}
	var bookResp map[string]interface{}
	json.Unmarshal(rec.Body.Bytes(), &bookResp)
	if bookResp["symbol"] != "AAPL" {
		t.Errorf("expected symbol AAPL, got %v", bookResp["symbol"])
	}
	bids := bookResp["bids"].([]interface{})
	if len(bids) != 1 {
		t.Errorf("expected 1 bid level, got %d", len(bids))
	}
}

func TestSimulatorEndpoints(t *testing.T) {
	eng, hub, wal, cleanup := setupTestServer(t)
	defer cleanup()

	sim := NewMarketSimulator(eng, hub, wal)

	toggleHandler := func(w http.ResponseWriter, r *http.Request) {
		running := sim.Toggle()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]bool{"running": running})
	}

	statusHandler := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]bool{"running": sim.IsRunning()})
	}

	// 1. Initial status -> false
	req := httptest.NewRequest("GET", "/simulator/status", nil)
	rec := httptest.NewRecorder()
	statusHandler(rec, req)
	var statusResp map[string]bool
	json.Unmarshal(rec.Body.Bytes(), &statusResp)
	if statusResp["running"] != false {
		t.Errorf("expected simulator running: false initially, got %v", statusResp["running"])
	}

	// 2. Toggle -> true
	req = httptest.NewRequest("POST", "/simulator/toggle", nil)
	rec = httptest.NewRecorder()
	toggleHandler(rec, req)
	var toggleResp map[string]bool
	json.Unmarshal(rec.Body.Bytes(), &toggleResp)
	if toggleResp["running"] != true {
		t.Errorf("expected simulator running: true after toggle, got %v", toggleResp["running"])
	}

	// 3. Toggle again -> false
	req = httptest.NewRequest("POST", "/simulator/toggle", nil)
	rec = httptest.NewRecorder()
	toggleHandler(rec, req)
	json.Unmarshal(rec.Body.Bytes(), &toggleResp)
	if toggleResp["running"] != false {
		t.Errorf("expected simulator running: false after second toggle, got %v", toggleResp["running"])
	}
}

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

func TestPlaceOrderRejectsOversizedPayload(t *testing.T) {
	eng, hub, wal, cleanup := setupTestServer(t)
	defer cleanup()
	huge := strings.Repeat("x", 2*1024*1024)
	req := httptest.NewRequest("POST", "/order", strings.NewReader(`{"symbol":"AAPL","pad":"`+huge+`"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handlePlaceOrder(eng, hub, wal)(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for oversized body, got %d", rec.Code)
	}
}

func TestGetOrderBookRequiresSymbol(t *testing.T) {
	eng, _, _, cleanup := setupTestServer(t)
	defer cleanup()
	req := httptest.NewRequest("GET", "/orderbook", nil)
	rec := httptest.NewRecorder()
	handleGetOrderBook(eng)(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing symbol param, got %d", rec.Code)
	}
}

func TestOriginCheckRejectsLocalhostOnRemoteHost(t *testing.T) {
	eng, hub, wal, cleanup := setupTestServer(t)
	defer cleanup()
	h := requireAllowedOrigin(handlePlaceOrder(eng, hub, wal))
	req := httptest.NewRequest("POST", "/order", strings.NewReader(`{"symbol":"AAPL","side":0,"type":0,"price":1,"amount":1}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://localhost:3000")
	req.Host = "production-exchange.com"
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 when localhost origin attacks remote host, got %d", rec.Code)
	}
}

func TestPublishOrderEventsBroadcastsBookUpdateOnMatch(t *testing.T) {
	hub := NewHub()
	go hub.Run()
	defer hub.Stop()

	client := &Client{
		hub:  hub,
		send: make(chan []byte, 10),
	}
	hub.register <- client
	time.Sleep(10 * time.Millisecond)

	fn := publishOrderEvents(hub, "AAPL")
	// Simulate market sweep (trades occurred, rested = false)
	trades := []*engine.Trade{
		{Symbol: "AAPL", Amount: 5, Price: 15000},
	}
	fn(trades, false)

	// We should receive 2 messages: trades AND book_update
	receivedTrades := false
	receivedBookUpdate := false
	timeout := time.After(500 * time.Millisecond)
	for i := 0; i < 2; i++ {
		select {
		case msg := <-client.send:
			var m map[string]interface{}
			json.Unmarshal(msg, &m)
			if m["type"] == "trades" {
				receivedTrades = true
			}
			if m["type"] == "book_update" {
				receivedBookUpdate = true
			}
		case <-timeout:
			t.Fatalf("timed out waiting for events (trades=%v, book_update=%v)", receivedTrades, receivedBookUpdate)
		}
	}
	if !receivedTrades || !receivedBookUpdate {
		t.Fatalf("expected both trades and book_update, got trades=%v, book_update=%v", receivedTrades, receivedBookUpdate)
	}
}

func TestHandleGetTrades(t *testing.T) {
	eng, hub, wal, cleanup := setupTestServer(t)
	defer cleanup()

	// 1. Missing symbol -> 400
	req := httptest.NewRequest("GET", "/trades", nil)
	rec := httptest.NewRecorder()
	handleGetTrades(eng, hub)(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing symbol, got %d", rec.Code)
	}

	// 2. Unknown symbol -> 404
	req = httptest.NewRequest("GET", "/trades?symbol=UNKNOWN", nil)
	rec = httptest.NewRecorder()
	handleGetTrades(eng, hub)(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown symbol, got %d", rec.Code)
	}

	// 3. Known symbol with trades
	placeHandler := handlePlaceOrder(eng, hub, wal)
	// Resting ask
	req = httptest.NewRequest("POST", "/order", strings.NewReader(`{"symbol":"AAPL","side":1,"type":0,"price":15000,"amount":10}`))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	placeHandler(rec, req)

	// Matching buy
	req = httptest.NewRequest("POST", "/order", strings.NewReader(`{"symbol":"AAPL","side":0,"type":0,"price":15000,"amount":5}`))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	placeHandler(rec, req)

	// Query /trades
	req = httptest.NewRequest("GET", "/trades?symbol=AAPL&limit=10", nil)
	rec = httptest.NewRecorder()
	handleGetTrades(eng, hub)(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for /trades, got %d", rec.Code)
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode /trades response: %v", err)
	}
	trades := resp["trades"].([]interface{})
	if len(trades) != 1 {
		t.Fatalf("expected 1 trade in /trades response, got %d", len(trades))
	}
}

func TestHandleGetOrders(t *testing.T) {
	eng, hub, wal, cleanup := setupTestServer(t)
	defer cleanup()

	// 1. Missing symbol -> 400
	req := httptest.NewRequest("GET", "/orders", nil)
	rec := httptest.NewRecorder()
	handleGetOrders(eng)(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing symbol, got %d", rec.Code)
	}

	// 2. Unknown symbol -> 404
	req = httptest.NewRequest("GET", "/orders?symbol=UNKNOWN", nil)
	rec = httptest.NewRecorder()
	handleGetOrders(eng)(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown symbol, got %d", rec.Code)
	}

	// 3. Place resting order and fetch /orders
	placeHandler := handlePlaceOrder(eng, hub, wal)
	req = httptest.NewRequest("POST", "/order", strings.NewReader(`{"symbol":"AAPL","side":0,"type":0,"price":14000,"amount":8}`))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	placeHandler(rec, req)

	req = httptest.NewRequest("GET", "/orders?symbol=AAPL", nil)
	rec = httptest.NewRecorder()
	handleGetOrders(eng)(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for /orders, got %d", rec.Code)
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode /orders response: %v", err)
	}
	orders := resp["orders"].([]interface{})
	if len(orders) != 1 {
		t.Fatalf("expected 1 resting order in /orders response, got %d", len(orders))
	}
}

func TestHandleCancelOrderBroadcastsBothEvents(t *testing.T) {
	eng, hub, wal, cleanup := setupTestServer(t)
	defer cleanup()

	client := &Client{
		hub:  hub,
		send: make(chan []byte, 10),
	}
	hub.register <- client
	time.Sleep(10 * time.Millisecond)

	placeHandler := handlePlaceOrder(eng, hub, wal)
	cancelHandler := handleCancelOrder(eng, hub, wal)

	// Place order
	req := httptest.NewRequest("POST", "/order", strings.NewReader(`{"symbol":"AAPL","side":0,"type":0,"price":14000,"amount":8}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	placeHandler(rec, req)

	var placeResp map[string]interface{}
	json.Unmarshal(rec.Body.Bytes(), &placeResp)
	orderID := uint64(placeResp["order"].(map[string]interface{})["id"].(float64))

	// Drain book_update from placement
	select {
	case <-client.send:
	case <-time.After(500 * time.Millisecond):
	}

	// Cancel order
	cancelURL := fmt.Sprintf("/order?symbol=AAPL&id=%d", orderID)
	req = httptest.NewRequest("DELETE", cancelURL, nil)
	rec = httptest.NewRecorder()
	cancelHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for cancel, got %d", rec.Code)
	}

	// Must receive order_cancelled AND book_update
	receivedCancelled := false
	receivedBookUpdate := false
	timeout := time.After(500 * time.Millisecond)
	for i := 0; i < 2; i++ {
		select {
		case msg := <-client.send:
			var m map[string]interface{}
			json.Unmarshal(msg, &m)
			if m["type"] == "order_cancelled" {
				receivedCancelled = true
			}
			if m["type"] == "book_update" {
				receivedBookUpdate = true
			}
		case <-timeout:
			t.Fatalf("timed out waiting for cancel broadcast events (cancelled=%v, book_update=%v)", receivedCancelled, receivedBookUpdate)
		}
	}

	if !receivedCancelled || !receivedBookUpdate {
		t.Fatalf("expected both order_cancelled and book_update, got cancelled=%v, book_update=%v", receivedCancelled, receivedBookUpdate)
	}
}

func TestHandleCancelOrderInvalidID(t *testing.T) {
	eng, hub, wal, cleanup := setupTestServer(t)
	defer cleanup()

	cancelHandler := handleCancelOrder(eng, hub, wal)

	// Non-numeric ID
	req := httptest.NewRequest("DELETE", "/order?symbol=AAPL&id=abc", nil)
	rec := httptest.NewRecorder()
	cancelHandler(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for non-numeric ID, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "positive integer") {
		t.Fatalf("expected message about positive integer, got %s", rec.Body.String())
	}

	// Zero ID
	req = httptest.NewRequest("DELETE", "/order?symbol=AAPL&id=0", nil)
	rec = httptest.NewRecorder()
	cancelHandler(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for zero ID, got %d", rec.Code)
	}

	// Missing ID (only symbol provided)
	req = httptest.NewRequest("DELETE", "/order?symbol=AAPL", nil)
	rec = httptest.NewRecorder()
	cancelHandler(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing ID, got %d", rec.Code)
	}
}

func TestHandleOptionsPreflight(t *testing.T) {
	// 1. Allowed origin preflight returns 204 with CORS headers
	req := httptest.NewRequest("OPTIONS", "/order", nil)
	req.Header.Set("Origin", "http://localhost:8080")
	req.Host = "localhost:8080"
	rec := httptest.NewRecorder()
	handleOptions(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204 No Content, got %d", rec.Code)
	}
	if rec.Header().Get("Access-Control-Allow-Origin") != "http://localhost:8080" {
		t.Errorf("expected Allow-Origin header, got %s", rec.Header().Get("Access-Control-Allow-Origin"))
	}

	// 2. Disallowed origin preflight returns 403
	reqDisallowed := httptest.NewRequest("OPTIONS", "/order", nil)
	reqDisallowed.Header.Set("Origin", "http://malicious.org")
	reqDisallowed.Host = "internal-exchange.com"
	recDisallowed := httptest.NewRecorder()
	handleOptions(recDisallowed, reqDisallowed)

	if recDisallowed.Code != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden for disallowed origin, got %d", recDisallowed.Code)
	}
}


