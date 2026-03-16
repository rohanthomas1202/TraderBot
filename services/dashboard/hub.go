package dashboard

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

// Client represents a connected WebSocket client.
type Client struct {
	conn *websocket.Conn
	send chan []byte
}

// Hub manages WebSocket clients and broadcasts messages.
type Hub struct {
	mu         sync.RWMutex
	clients    map[*Client]struct{}
	broadcast  chan []byte
	register   chan *Client
	unregister chan *Client
	logger     *slog.Logger
}

// NewHub creates a new WebSocket hub.
func NewHub(logger *slog.Logger) *Hub {
	return &Hub{
		clients:    make(map[*Client]struct{}),
		broadcast:  make(chan []byte, 256),
		register:   make(chan *Client),
		unregister: make(chan *Client),
		logger:     logger,
	}
}

// Run starts the hub's main loop. Blocks until ctx is cancelled.
func (h *Hub) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			h.mu.Lock()
			for c := range h.clients {
				close(c.send)
				delete(h.clients, c)
			}
			h.mu.Unlock()
			return

		case client := <-h.register:
			h.mu.Lock()
			h.clients[client] = struct{}{}
			h.mu.Unlock()
			h.logger.Debug("client connected", "clients", h.ClientCount())

		case client := <-h.unregister:
			h.mu.Lock()
			if _, ok := h.clients[client]; ok {
				close(client.send)
				delete(h.clients, client)
			}
			h.mu.Unlock()
			h.logger.Debug("client disconnected", "clients", h.ClientCount())

		case msg := <-h.broadcast:
			h.mu.RLock()
			for client := range h.clients {
				select {
				case client.send <- msg:
				default:
					// Client send buffer full — drop them
					go func(c *Client) { h.unregister <- c }(client)
				}
			}
			h.mu.RUnlock()
		}
	}
}

// Broadcast sends a message to all connected clients.
func (h *Hub) Broadcast(data []byte) {
	select {
	case h.broadcast <- data:
	default:
		h.logger.Warn("broadcast channel full, dropping message")
	}
}

// ClientCount returns the number of connected clients.
func (h *Hub) ClientCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

// ServeClient handles a single WebSocket client connection.
func (h *Hub) ServeClient(ctx context.Context, conn *websocket.Conn) {
	client := &Client{
		conn: conn,
		send: make(chan []byte, 64),
	}
	h.register <- client
	defer func() { h.unregister <- client }()

	// Writer goroutine
	go func() {
		for msg := range client.send {
			ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			err := wsjson.Write(ctx, conn, json.RawMessage(msg))
			cancel()
			if err != nil {
				return
			}
		}
	}()

	// Reader loop (keep alive, detect disconnects)
	for {
		_, _, err := conn.Read(ctx)
		if err != nil {
			return
		}
	}
}
