package main

import (
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/divya-3005/OME/server/engine"
	"github.com/gorilla/websocket"
)

const (
	writeWait      = 10 * time.Second
	pongWait       = 60 * time.Second
	pingPeriod     = (pongWait * 9) / 10
	maxMessageSize = 512
	sendBufferSize = 256
)

// isAllowedOrigin reports whether a request's Origin is trusted. Requests without an
// Origin header (curl, bots, tests) are allowed; browsers always send Origin on
// WebSocket upgrades and on non-GET requests.
func isAllowedOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	if u.Host == r.Host {
		return true
	}
	isLocalHost := r.Host == "localhost" || strings.HasPrefix(r.Host, "localhost:") ||
		r.Host == "127.0.0.1" || strings.HasPrefix(r.Host, "127.0.0.1:")
	if isLocalHost && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1") {
		return true
	}
	if allowed := os.Getenv("ALLOWED_ORIGINS"); allowed != "" {
		for _, a := range strings.Split(allowed, ",") {
			if strings.TrimSpace(a) == origin {
				return true
			}
		}
	}
	return false
}

var upgrader = websocket.Upgrader{CheckOrigin: isAllowedOrigin}

// Client is a middleman between the websocket connection and the hub
type Client struct {
	hub  *Hub
	conn *websocket.Conn
	send chan []byte
}

func (c *Client) readPump() {
	defer func() {
		select {
		case c.hub.unregister <- c:
		case <-c.hub.stop:
		}
		c.conn.Close()
	}()
	c.conn.SetReadLimit(maxMessageSize)
	c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error {
		c.conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})
	c.conn.SetPingHandler(func(string) error {
		c.conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})
	for {
		_, _, err := c.conn.ReadMessage()
		if err != nil {
			break
		}
	}
}

func (c *Client) writePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		c.conn.Close()
	}()
	for {
		select {
		case message, ok := <-c.send:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				// Hub closed the channel: write close frame from the dedicated writer goroutine
				_ = c.conn.WriteMessage(
					websocket.CloseMessage,
					websocket.FormatCloseMessage(websocket.CloseNormalClosure, "connection closed"),
				)
				return
			}
			// Exactly one JSON event per WebSocket frame.
			if err := c.conn.WriteMessage(websocket.TextMessage, message); err != nil {
				return
			}
		case <-ticker.C:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// Hub maintains the set of active WebSocket clients and broadcasts messages
type Hub struct {
	clients    map[*Client]bool
	broadcast  chan []byte
	register   chan *Client
	unregister chan *Client
	stop       chan struct{}
	stopOnce   sync.Once
	done       chan struct{}

	tradesMu     sync.RWMutex
	recentTrades map[string][]*engine.Trade
}

// NewHub creates a new Hub instance
func NewHub() *Hub {
	return &Hub{
		clients:      make(map[*Client]bool),
		broadcast:    make(chan []byte, 1024),
		register:     make(chan *Client),
		unregister:   make(chan *Client),
		stop:         make(chan struct{}),
		done:         make(chan struct{}),
		recentTrades: make(map[string][]*engine.Trade),
	}
}

// Run listens on channels and handles client connections & non-blocking broadcasts
func (h *Hub) Run() {
	defer close(h.done)
	for {
		select {
		case <-h.stop:
			for client := range h.clients {
				close(client.send)
				delete(h.clients, client)
			}
			return

		case client := <-h.register:
			h.clients[client] = true

		case client := <-h.unregister:
			if _, ok := h.clients[client]; ok {
				delete(h.clients, client)
				close(client.send)
			}

		case message := <-h.broadcast:
			for client := range h.clients {
				select {
				case client.send <- message:
				default:
					// Slow or wedged client buffer full: drop and unregister without blocking the hub
					close(client.send)
					delete(h.clients, client)
				}
			}
		}
	}
}

// Stop cleanly notifies and closes all connected clients, terminating the hub loop.
func (h *Hub) Stop() {
	h.stopOnce.Do(func() {
		close(h.stop)
	})
	<-h.done
}

// BroadcastJSON serializes any event to JSON and sends it to all clients.
// It is non-blocking to prevent broadcast backpressure from ever blocking
// the caller (such as an order book holding its mutex).
func (h *Hub) BroadcastJSON(v interface{}) {
	data, err := json.Marshal(v)
	if err != nil {
		log.Printf("json marshal error: %v", err)
		return
	}
	select {
	case <-h.stop:
		return
	case h.broadcast <- data:
	default:
		log.Printf("hub: broadcast queue full (%d), dropping event to protect engine throughput", cap(h.broadcast))
	}
}

// RecordTrades appends executed trades to the symbol's in-memory trade history buffer.
func (h *Hub) RecordTrades(symbol string, trades []*engine.Trade) {
	if len(trades) == 0 {
		return
	}
	h.tradesMu.Lock()
	defer h.tradesMu.Unlock()

	list := h.recentTrades[symbol]
	list = append(list, trades...)
	if len(list) > 200 {
		list = list[len(list)-200:]
	}
	h.recentTrades[symbol] = list
}

// GetRecentTrades returns a copy of the recent trades recorded for a symbol.
func (h *Hub) GetRecentTrades(symbol string) []*engine.Trade {
	h.tradesMu.RLock()
	defer h.tradesMu.RUnlock()

	list := h.recentTrades[symbol]
	if len(list) == 0 {
		return []*engine.Trade{}
	}
	res := make([]*engine.Trade, len(list))
	copy(res, list)
	return res
}
