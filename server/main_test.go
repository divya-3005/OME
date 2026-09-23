package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

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
	rec = httptest.NewRecorder()
	handler(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for unknown symbol, got %d", rec.Code)
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
