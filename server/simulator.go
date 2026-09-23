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
