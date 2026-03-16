package dashboard

import (
	"encoding/json"
	"log/slog"

	"autonomy-platform/internal/events"

	"github.com/nats-io/nats.go"
)

// WSMessage is the envelope for WebSocket messages.
type WSMessage struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

// NATSBridge subscribes to NATS events and forwards them to the WebSocket hub.
type NATSBridge struct {
	sub    *events.Subscriber
	hub    *Hub
	logger *slog.Logger
}

// NewNATSBridge creates a bridge from NATS events to the WebSocket hub.
func NewNATSBridge(sub *events.Subscriber, hub *Hub, logger *slog.Logger) *NATSBridge {
	return &NATSBridge{sub: sub, hub: hub, logger: logger}
}

// Start subscribes to all relevant NATS subjects.
func (b *NATSBridge) Start() error {
	subjects := []struct {
		subject  string
		consumer string
	}{
		{"order.>", "dashboard-orders"},
		{"risk.>", "dashboard-risk"},
		{"kill.>", "dashboard-kill"},
		{"system.heartbeat.>", "dashboard-heartbeat"},
		{"data.market.>", "dashboard-data"},
	}

	for _, s := range subjects {
		subject := s.subject
		if _, err := b.sub.SubscribeNew(s.subject, s.consumer, func(msg *nats.Msg) {
			b.forward(subject, msg)
			msg.Ack()
		}); err != nil {
			return err
		}
	}
	return nil
}

func (b *NATSBridge) forward(subject string, msg *nats.Msg) {
	envelope := WSMessage{
		Type: msg.Subject,
		Data: json.RawMessage(msg.Data),
	}
	data, err := json.Marshal(envelope)
	if err != nil {
		b.logger.Error("failed to marshal ws message", "error", err)
		return
	}
	b.hub.Broadcast(data)
}
