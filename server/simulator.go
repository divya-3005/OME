package main

import (
	"math/rand"
	"sync"
	"time"

	"github.com/divya-3005/OME/server/engine"
)

type MarketSimulator struct {
	eng     *engine.Engine
	hub     *Hub
	wal     *engine.WAL
	running bool
	mu      sync.Mutex
	stop    chan struct{}
}

func NewMarketSimulator(eng *engine.Engine, hub *Hub, wal *engine.WAL) *MarketSimulator {
	return &MarketSimulator{
		eng:  eng,
		hub:  hub,
		wal:  wal,
		stop: make(chan struct{}),
	}
}

// SeedSymbol pre-populates realistic bids and asks for a single symbol
func (sim *MarketSimulator) SeedSymbol(symbol string) {
	var midPrice uint64
	switch symbol {
	case "AAPL":
		midPrice = 15000 // $150.00
	case "TSLA":
		midPrice = 24000 // $240.00
	case "BTC-USD":
		midPrice = 6400000 // $64,000.00
	default:
		return
	}

	// Create 10 Bids below mid price
	for i := 1; i <= 10; i++ {
		orderID := sim.eng.NextOrderID()
		diff := uint64(i * 10)
		qty := uint64(5 + rand.Intn(25))
		order := &engine.Order{
			ID:        orderID,
			Symbol:    symbol,
			Side:      engine.Buy,
			Type:      engine.Limit,
			Price:     midPrice - diff,
			Amount:    qty,
			Timestamp: time.Now().UnixNano(),
		}
		sim.eng.ProcessOrderWithWAL(order, sim.wal)
	}

	// Create 10 Asks above mid price
	for i := 1; i <= 10; i++ {
		orderID := sim.eng.NextOrderID()
		diff := uint64(i * 10)
		qty := uint64(5 + rand.Intn(25))
		order := &engine.Order{
			ID:        orderID,
			Symbol:    symbol,
			Side:      engine.Sell,
			Type:      engine.Limit,
			Price:     midPrice + diff,
			Amount:    qty,
			Timestamp: time.Now().UnixNano(),
		}
		sim.eng.ProcessOrderWithWAL(order, sim.wal)
	}
}

// SeedMarket pre-populates realistic bids and asks for all symbols
func (sim *MarketSimulator) SeedMarket() {
	for _, sym := range []string{"AAPL", "TSLA", "BTC-USD"} {
		sim.SeedSymbol(sym)
	}
}

// Start begins periodic order placement to simulate live market trading
func (sim *MarketSimulator) Start() {
	sim.mu.Lock()
	if sim.running {
		sim.mu.Unlock()
		return
	}
	sim.running = true
	sim.stop = make(chan struct{})
	sim.mu.Unlock()

	go func() {
		ticker := time.NewTicker(600 * time.Millisecond)
		defer ticker.Stop()

		symbols := []string{"AAPL", "TSLA", "BTC-USD"}

		for {
			select {
			case <-sim.stop:
				return
			case <-ticker.C:
				orderID := sim.eng.NextOrderID()
				symbol := symbols[rand.Intn(len(symbols))]
				ob, exists := sim.eng.GetOrderBook(symbol)
				if !exists {
					continue
				}

				bestBid, bestAsk, ok := ob.GetBestBidAsk()
				if !ok {
					continue
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
							price = 1 // Safe positive price: prevents uint64 underflow
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
					ID:        orderID,
					Symbol:    symbol,
					Side:      side,
					Type:      orderType,
					Price:     price,
					Amount:    qty,
					Timestamp: time.Now().UnixNano(),
				}

				trades, err := sim.eng.ProcessOrderWithWAL(order, sim.wal)
				if err == nil && len(trades) > 0 {
					sim.hub.BroadcastJSON(map[string]interface{}{
						"type":   "trades",
						"symbol": symbol,
						"data":   trades,
					})
				}
			}
		}
	}()
}

// Toggle toggles the simulator on or off
func (sim *MarketSimulator) Toggle() bool {
	sim.mu.Lock()
	if sim.running {
		close(sim.stop)
		sim.running = false
		sim.mu.Unlock()
		return false
	}

	sim.mu.Unlock()
	sim.Start()
	return true
}

func (sim *MarketSimulator) IsRunning() bool {
	sim.mu.Lock()
	defer sim.mu.Unlock()
	return sim.running
}
