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

// SeedMarket pre-populates realistic bids and asks for all symbols
func (sim *MarketSimulator) SeedMarket() {
	symbols := []struct {
		name     string
		midPrice uint64 // in cents
	}{
		{"AAPL", 15000},     // $150.00
		{"TSLA", 24000},     // $240.00
		{"BTC-USD", 6400000}, // $64,000.00
	}

	orderID := uint64(1000)

	for _, s := range symbols {
		// Create 10 Bids below mid price
		for i := 1; i <= 10; i++ {
			orderID++
			diff := uint64(i * 10)
			qty := uint64(5 + rand.Intn(25))
			order := &engine.Order{
				ID:        orderID,
				Symbol:    s.name,
				Side:      engine.Buy,
				Type:      engine.Limit,
				Price:     s.midPrice - diff,
				Amount:    qty,
				Timestamp: time.Now().UnixNano(),
			}
			sim.wal.LogPlace(order)
			sim.eng.ProcessOrder(order)
		}

		// Create 10 Asks above mid price
		for i := 1; i <= 10; i++ {
			orderID++
			diff := uint64(i * 10)
			qty := uint64(5 + rand.Intn(25))
			order := &engine.Order{
				ID:        orderID,
				Symbol:    s.name,
				Side:      engine.Sell,
				Type:      engine.Limit,
				Price:     s.midPrice + diff,
				Amount:    qty,
				Timestamp: time.Now().UnixNano(),
			}
			sim.wal.LogPlace(order)
			sim.eng.ProcessOrder(order)
		}
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
		orderID := uint64(50000)

		symbols := []string{"AAPL", "TSLA", "BTC-USD"}

		for {
			select {
			case <-sim.stop:
				return
			case <-ticker.C:
				orderID++
				symbol := symbols[rand.Intn(len(symbols))]
				ob, exists := sim.eng.GetOrderBook(symbol)
				if !exists || len(ob.Bids) == 0 || len(ob.Asks) == 0 {
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
						price = ob.Asks[0].Price
					} else {
						price = ob.Bids[0].Price - uint64(rand.Intn(30))
					}
				} else {
					// Sell near best bid to trigger trade, or near best ask to add liquidity
					if isMarket || rand.Float32() < 0.4 {
						price = ob.Bids[0].Price
					} else {
						price = ob.Asks[0].Price + uint64(rand.Intn(30))
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

				sim.wal.LogPlace(order)
				trades, err := sim.eng.ProcessOrder(order)
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
	defer sim.mu.Unlock()

	if sim.running {
		close(sim.stop)
		sim.running = false
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
