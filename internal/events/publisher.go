package events

import (
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/nats-io/nats.go"
)

// Publisher wraps NATS JetStream for event publishing.
type Publisher struct {
	js     nats.JetStreamContext
	logger *slog.Logger
}

func NewPublisher(nc *nats.Conn) (*Publisher, error) {
	js, err := nc.JetStream()
	if err != nil {
		return nil, fmt.Errorf("create jetstream context: %w", err)
	}

	// Ensure streams exist. Idempotent — no error if already created.
	streams := []struct {
		Name     string
		Subjects []string
	}{
		{"ORDERS", []string{"order.>"}},
		{"RISK", []string{"risk.>"}},
		{"KILL", []string{"kill.>"}},
		{"SYSTEM", []string{"system.>"}},
		{"DATA", []string{"data.>"}},
	}

	for _, s := range streams {
		_, err := js.AddStream(&nats.StreamConfig{
			Name:     s.Name,
			Subjects: s.Subjects,
			MaxAge:   90 * 24 * 60 * 60 * 1e9, // 90 days in nanoseconds
			Storage:  nats.FileStorage,
		})
		if err != nil {
			return nil, fmt.Errorf("create stream %s: %w", s.Name, err)
		}
	}

	return &Publisher{
		js:     js,
		logger: slog.Default().With("component", "events"),
	}, nil
}

// Publish serializes the payload as JSON and publishes to the subject.
func (p *Publisher) Publish(subject string, payload interface{}) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}

	_, err = p.js.Publish(subject, data)
	if err != nil {
		p.logger.Error("event publish failed", "subject", subject, "error", err)
		return fmt.Errorf("publish to %s: %w", subject, err)
	}

	p.logger.Debug("event published", "subject", subject, "size", len(data))
	return nil
}
