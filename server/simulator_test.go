package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/divya-3005/OME/server/engine"
)

func setupTestSimulator(t *testing.T) (*MarketSimulator, *engine.Engine, *Hub, *engine.WAL, func()) {
	t.Helper()
	eng := engine.NewEngine()
	hub := NewHub()
	go hub.Run()

	tempDir := t.TempDir()
	walPath := filepath.Join(tempDir, "sim_test_wal.log")
	wal, err := engine.OpenWAL(walPath)
	if err != nil {
		t.Fatalf("failed to open test WAL: %v", err)
	}

	sim := NewMarketSimulator(eng, hub, wal)

	cleanup := func() {
		sim.Stop() // waits for the goroutine to exit before the WAL is closed
		hub.Stop()
		wal.Close()
		os.Remove(walPath)
	}

	return sim, eng, hub, wal, cleanup
}

func TestSimulatorSeedSymbol(t *testing.T) {
	sim, eng, _, _, cleanup := setupTestSimulator(t)
	defer cleanup()

	eng.RegisterSymbol("AAPL")
	eng.RegisterSymbol("TSLA")
	eng.RegisterSymbol("BTC-USD")

	// 1. Seed AAPL
	sim.SeedSymbol("AAPL")

	ob, exists := eng.GetOrderBook("AAPL")
	if !exists {
		t.Fatalf("expected AAPL order book to exist")
	}

	bids, asks := ob.GetSnapshot()
	if len(bids) != 10 {
		t.Errorf("expected 10 bids seeded for AAPL, got %d", len(bids))
	}
	if len(asks) != 10 {
		t.Errorf("expected 10 asks seeded for AAPL, got %d", len(asks))
	}

	// 2. Unsupported symbol should be gracefully ignored
	sim.SeedSymbol("XYZ_COIN")
	if _, exists := eng.GetOrderBook("XYZ_COIN"); exists {
		t.Errorf("expected XYZ_COIN to not be registered")
	}
}

func TestSimulatorSeedMarket(t *testing.T) {
	sim, eng, _, _, cleanup := setupTestSimulator(t)
	defer cleanup()

	for _, sym := range []string{"AAPL", "TSLA", "BTC-USD"} {
		eng.RegisterSymbol(sym)
	}

	sim.SeedMarket()

	for _, sym := range []string{"AAPL", "TSLA", "BTC-USD"} {
		ob, exists := eng.GetOrderBook(sym)
		if !exists {
			t.Fatalf("expected order book for %s to exist", sym)
		}
		bids, asks := ob.GetSnapshot()
		if len(bids) == 0 || len(asks) == 0 {
			t.Errorf("expected seeded bids/asks for %s, got %d bids, %d asks", sym, len(bids), len(asks))
		}
	}
}

func TestSimulatorStartStopToggle(t *testing.T) {
	sim, _, _, _, cleanup := setupTestSimulator(t)
	defer cleanup()

	if sim.IsRunning() {
		t.Errorf("expected simulator to initially be stopped")
	}

	// 1. Toggle ON
	running := sim.Toggle()
	if !running || !sim.IsRunning() {
		t.Errorf("expected simulator to be running after toggle on")
	}

	// 2. Calling Start() while already running is idempotent
	sim.Start()
	if !sim.IsRunning() {
		t.Errorf("expected simulator to remain running after redundant Start()")
	}

	// 3. Toggle OFF
	running = sim.Toggle()
	if running || sim.IsRunning() {
		t.Errorf("expected simulator to be stopped after toggle off")
	}
}

func TestSimulatorLiveOrderExecution(t *testing.T) {
	sim, eng, hub, _, cleanup := setupTestSimulator(t)
	defer cleanup()

	for _, sym := range []string{"AAPL", "TSLA", "BTC-USD"} {
		eng.RegisterSymbol(sym)
	}
	sim.SeedMarket()

	// Register a mock client to the hub to capture broadcast trades
	client := &Client{
		hub:  hub,
		send: make(chan []byte, 100),
	}
	hub.register <- client
	time.Sleep(10 * time.Millisecond)

	// Start live simulation
	sim.Start()

	// Wait up to 10 seconds for at least one trade broadcast
	receivedTrade := false
	timeout := time.After(10 * time.Second)

	for !receivedTrade {
		select {
		case msg := <-client.send:
			var event map[string]interface{}
			if err := json.Unmarshal(msg, &event); err == nil {
				if event["type"] == "trades" {
					receivedTrade = true
				}
			}
		case <-timeout:
			t.Fatalf("timed out waiting for simulator to generate and broadcast a trade")
		}
	}

	sim.Stop()
}

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

func TestSimulatorNarrowsWideSpread(t *testing.T) {
	sim, eng, _, wal, cleanup := setupTestSimulator(t)
	defer cleanup()
	eng.RegisterSymbol("AAPL")

	ob, _ := eng.GetOrderBook("AAPL")
	// Seed a very wide spread: bid at 14000, ask at 16000 (spread = 2000, step = 10)
	ob.ProcessOrderWithWAL(&engine.Order{
		ID:        1,
		Symbol:    "AAPL",
		Side:      engine.Buy,
		Type:      engine.Limit,
		Price:     14000,
		Amount:    10,
		Timestamp: time.Now().UnixMilli(),
	}, wal)
	ob.ProcessOrderWithWAL(&engine.Order{
		ID:        2,
		Symbol:    "AAPL",
		Side:      engine.Sell,
		Type:      engine.Limit,
		Price:     16000,
		Amount:    10,
		Timestamp: time.Now().UnixMilli(),
	}, wal)
	eng.SetMinOrderID(2)

	// Execute several simulation steps
	for i := 0; i < 20; i++ {
		sim.step()
	}

	bid, hasBid, ask, hasAsk := ob.TopOfBook()
	if !hasBid || !hasAsk {
		t.Fatalf("expected both bid and ask to exist")
	}
	// Spread should have tightened within the 14000-16000 corridor without widening further
	if bid < 14000 {
		t.Fatalf("best bid widened beyond initial 14000: %d", bid)
	}
	if ask > 16000 {
		t.Fatalf("best ask widened beyond initial 16000: %d", ask)
	}
	spread := ask - bid
	if spread >= 2000 {
		t.Fatalf("expected spread to narrow from 2000, got spread=%d (bid=%d, ask=%d)", spread, bid, ask)
	}
}

