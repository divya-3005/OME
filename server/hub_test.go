package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestCheckOrigin(t *testing.T) {
	tests := []struct {
		name           string
		origin         string
		host           string
		allowedOrigins string
		expected       bool
	}{
		{
			name:     "no origin header (CLI / bot)",
			origin:   "",
			host:     "localhost:8080",
			expected: true,
		},
		{
			name:     "localhost origin",
			origin:   "http://localhost:8080",
			host:     "localhost:8080",
			expected: true,
		},
		{
			name:     "127.0.0.1 origin",
			origin:   "http://127.0.0.1:8080",
			host:     "127.0.0.1:8080",
			expected: true,
		},
		{
			name:     "same host match",
			origin:   "http://myexchange.internal:9000",
			host:     "myexchange.internal:9000",
			expected: true,
		},
		{
			name:     "disallowed origin",
			origin:   "http://malicious-site.com",
			host:     "production-api.com",
			expected: false,
		},
		{
			name:     "malformed origin URL",
			origin:   "://not-a-valid-url",
			host:     "localhost:8080",
			expected: false,
		},
		{
			name:           "custom allowed origin via env var",
			origin:         "https://trading.company.org",
			host:           "internal-api.com",
			allowedOrigins: "https://foo.com,https://trading.company.org",
			expected:       true,
		},
		{
			name:           "env var set but origin not in list",
			origin:         "https://unauthorized.org",
			host:           "internal-api.com",
			allowedOrigins: "https://foo.com,https://bar.com",
			expected:       false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.allowedOrigins != "" {
				os.Setenv("ALLOWED_ORIGINS", tc.allowedOrigins)
				defer os.Unsetenv("ALLOWED_ORIGINS")
			} else {
				os.Unsetenv("ALLOWED_ORIGINS")
			}

			req, err := http.NewRequest("GET", "/ws", nil)
			if err != nil {
				t.Fatalf("failed to create request: %v", err)
			}
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			req.Host = tc.host

			got := upgrader.CheckOrigin(req)
			if got != tc.expected {
				t.Errorf("CheckOrigin(%q with host %q) = %v; want %v", tc.origin, tc.host, got, tc.expected)
			}
		})
	}
}

func TestHubLifecycleAndBroadcast(t *testing.T) {
	hub := NewHub()
	go hub.Run()

	client := &Client{
		hub:  hub,
		send: make(chan []byte, 10),
	}

	// 1. Register client
	hub.register <- client

	// Give a moment for register to process in hub goroutine
	time.Sleep(10 * time.Millisecond)

	// 2. Broadcast JSON message
	testPayload := map[string]string{"type": "trades", "symbol": "AAPL"}
	hub.BroadcastJSON(testPayload)

	select {
	case msg, ok := <-client.send:
		if !ok {
			t.Fatalf("client.send was unexpectedly closed")
		}
		var received map[string]string
		if err := json.Unmarshal(msg, &received); err != nil {
			t.Fatalf("failed to unmarshal broadcast message: %v", err)
		}
		if received["symbol"] != "AAPL" || received["type"] != "trades" {
			t.Errorf("unexpected message received: %+v", received)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("timed out waiting for broadcast message")
	}

	// 3. Unregister client
	hub.unregister <- client

	select {
	case _, ok := <-client.send:
		if ok {
			t.Errorf("expected client.send to be closed after unregistering")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("timed out waiting for client.send channel close")
	}
}

func TestHubSlowClientDrop(t *testing.T) {
	hub := NewHub()
	go hub.Run()

	// Client with tiny buffer of 1
	slowClient := &Client{
		hub:  hub,
		send: make(chan []byte, 1),
	}

	hub.register <- slowClient
	time.Sleep(20 * time.Millisecond)

	// Fill the buffer
	slowClient.send <- []byte("first_message")

	// Broadcast another message — buffer is full, hub should drop and close slowClient
	hub.broadcast <- []byte("second_message")

	// Allow the hub's select loop time to evaluate client.send and execute default drop
	time.Sleep(50 * time.Millisecond)

	// Drain "first_message"
	select {
	case msg, ok := <-slowClient.send:
		if !ok || string(msg) != "first_message" {
			t.Fatalf("expected first_message, got msg=%s, ok=%v", string(msg), ok)
		}
	default:
		t.Fatalf("expected first_message to be buffered")
	}

	// Verify channel was closed by hub
	select {
	case _, ok := <-slowClient.send:
		if ok {
			t.Errorf("expected channel to be closed after dropping slow client")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("timed out waiting for slow client channel closure")
	}
}

func TestWebSocketIntegration(t *testing.T) {
	hub := NewHub()
	go hub.Run()

	server := httptest.NewServer(handleWebSocket(hub))
	defer server.Close()

	// Convert http URL to ws URL
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")

	dialer := websocket.Dialer{}
	conn, resp, err := dialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("failed to dial websocket: %v (resp: %v)", err, resp)
	}
	defer conn.Close()

	// Give a moment for registration
	time.Sleep(20 * time.Millisecond)

	// Broadcast an event through the hub
	hub.BroadcastJSON(map[string]interface{}{
		"type":   "trades",
		"symbol": "TSLA",
		"price":  24000,
	})

	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	messageType, p, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("failed to read from websocket: %v", err)
	}
	if messageType != websocket.TextMessage {
		t.Errorf("expected TextMessage (%d), got %d", websocket.TextMessage, messageType)
	}

	var data map[string]interface{}
	if err := json.Unmarshal(p, &data); err != nil {
		t.Fatalf("failed to parse message JSON: %v", err)
	}
	if data["symbol"] != "TSLA" {
		t.Errorf("expected symbol TSLA, got %v", data["symbol"])
	}
}

func TestWebSocketOneEventPerFrame(t *testing.T) {
	hub := NewHub()
	go hub.Run()
	server := httptest.NewServer(handleWebSocket(hub))
	defer server.Close()

	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	time.Sleep(20 * time.Millisecond)

	for i := 0; i < 20; i++ {
		hub.BroadcastJSON(map[string]int{"seq": i})
	}
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	for i := 0; i < 20; i++ {
		_, p, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read %d: %v", i, err)
		}
		var m map[string]int
		if err := json.Unmarshal(p, &m); err != nil {
			t.Fatalf("frame %d is not a single JSON object: %q", i, p)
		}
		if m["seq"] != i {
			t.Fatalf("out of order: got %d want %d", m["seq"], i)
		}
	}
}

func TestHubBroadcastNonBlockingWhenFull(t *testing.T) {
	hub := NewHub()
	// Do NOT run hub.Run(), so broadcast channel never gets drained

	// Fill the broadcast buffer completely (capacity 1024)
	for i := 0; i < 1024; i++ {
		hub.broadcast <- []byte("fill")
	}

	done := make(chan struct{})
	go func() {
		hub.BroadcastJSON(map[string]string{"type": "trades"})
		close(done)
	}()

	select {
	case <-done:
		// Succeeded immediately without blocking
	case <-time.After(500 * time.Millisecond):
		t.Fatal("BroadcastJSON blocked on a full broadcast channel")
	}
}

func TestHubStopClosesClients(t *testing.T) {
	hub := NewHub()
	go hub.Run()

	server := httptest.NewServer(handleWebSocket(hub))
	defer server.Close()

	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	time.Sleep(20 * time.Millisecond)

	// Stop the hub
	hub.Stop()

	// Client should read a CloseMessage or encounter closed socket
	conn.SetReadDeadline(time.Now().Add(1 * time.Second))
	_, _, err = conn.ReadMessage()
	if err == nil {
		t.Fatal("expected error reading from closed connection, got nil")
	}
	if !websocket.IsCloseError(err, websocket.CloseNormalClosure) && !strings.Contains(err.Error(), "closed") {
		t.Logf("connection closed as expected: %v", err)
	}
}
