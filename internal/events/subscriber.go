package events

import (
	"fmt"
	"log/slog"

	"github.com/nats-io/nats.go"
)

// Subscriber wraps NATS JetStream for durable event consumption.
type Subscriber struct {
	js     nats.JetStreamContext
	logger *slog.Logger
}

func NewSubscriber(nc *nats.Conn) (*Subscriber, error) {
	js, err := nc.JetStream()
	if err != nil {
		return nil, fmt.Errorf("create jetstream context: %w", err)
	}
	return &Subscriber{
		js:     js,
		logger: slog.Default().With("component", "events"),
	}, nil
}

// Subscribe creates a durable pull subscription.
// consumerName should be unique per service (e.g., "risk-engine-orders").
func (s *Subscriber) Subscribe(subject, consumerName string, handler nats.MsgHandler) (*nats.Subscription, error) {
	sub, err := s.js.Subscribe(subject, handler,
		nats.Durable(consumerName),
		nats.DeliverAll(),
		nats.AckExplicit(),
	)
	if err != nil {
		return nil, fmt.Errorf("subscribe to %s: %w", subject, err)
	}
	s.logger.Info("subscribed", "subject", subject, "consumer", consumerName)
	return sub, nil
}
