package alertbot

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"autonomy-platform/internal/events"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/nats-io/nats.go"
)

// ChatGroups holds chat IDs for different notification categories.
type ChatGroups struct {
	Trades []int64 // proposed, approved, submitted, filled
	Alerts []int64 // denied, kill switch
}

const (
	batchSize     = 500
	batchInterval = 1 * time.Minute
)

// Notifier subscribes to NATS events and pushes Telegram notifications.
type Notifier struct {
	bot    *tgbotapi.BotAPI
	sub    *events.Subscriber
	groups ChatGroups
	logger *slog.Logger

	mu       sync.Mutex
	proposed []events.OrderProposedEvent
	denied   []events.OrderDeniedEvent
}

// NewNotifier creates a push notifier. If groups has empty Trades or Alerts,
// those categories fall back to the default chatIDs.
func NewNotifier(bot *tgbotapi.BotAPI, sub *events.Subscriber, chatIDs []int64, groups ChatGroups, logger *slog.Logger) *Notifier {
	if len(groups.Trades) == 0 {
		groups.Trades = chatIDs
	}
	if len(groups.Alerts) == 0 {
		groups.Alerts = chatIDs
	}
	return &Notifier{
		bot:    bot,
		sub:    sub,
		groups: groups,
		logger: logger,
	}
}

// Start begins listening for events and sending notifications.
func (n *Notifier) Start() error {
	type subscription struct {
		subject  string
		consumer string
		handler  nats.MsgHandler
	}
	subs := []subscription{
		{"order.proposed.>", "alertbot-proposed", n.handleProposed},
		{"order.approved.>", "alertbot-approved", n.handleApproved},
		{"order.denied.>", "alertbot-denied", n.handleDenied},
		{"order.submitted.>", "alertbot-submitted", n.handleSubmitted},
		{"order.filled.>", "alertbot-fills", n.handleFill},
		{"kill.activated.>", "alertbot-kills", n.handleKill},
	}

	for _, s := range subs {
		if _, err := n.sub.SubscribeNew(s.subject, s.consumer, s.handler); err != nil {
			return err
		}
		n.logger.Info("notifier subscribed", "subject", s.subject)
	}

	// Start batch flush ticker
	go n.batchFlusher()

	return nil
}

// batchFlusher periodically flushes accumulated proposed/denied events.
func (n *Notifier) batchFlusher() {
	ticker := time.NewTicker(batchInterval)
	defer ticker.Stop()

	for range ticker.C {
		n.flushProposed()
		n.flushDenied()
	}
}

func (n *Notifier) flushProposed() {
	n.mu.Lock()
	batch := n.proposed
	n.proposed = nil
	n.mu.Unlock()

	if len(batch) == 0 {
		return
	}

	// Group by market
	byMarket := make(map[string]int)
	for _, e := range batch {
		byMarket[e.MarketID]++
	}

	msg := fmt.Sprintf("🟡 %d Orders Proposed\n", len(batch))
	for market, count := range byMarket {
		msg += fmt.Sprintf("  %s: %d\n", market, count)
	}

	n.broadcastTrades(msg)
}

func (n *Notifier) flushDenied() {
	n.mu.Lock()
	batch := n.denied
	n.denied = nil
	n.mu.Unlock()

	if len(batch) == 0 {
		return
	}

	// Group by reason
	byReason := make(map[string]int)
	for _, e := range batch {
		byReason[e.DenyReason]++
	}

	msg := fmt.Sprintf("❌ %d Orders Denied\n", len(batch))
	for reason, count := range byReason {
		msg += fmt.Sprintf("  %s: %d\n", reason, count)
	}

	n.broadcastAlerts(msg)
}

func (n *Notifier) broadcastTo(chatIDs []int64, text string) {
	for _, chatID := range chatIDs {
		msg := tgbotapi.NewMessage(chatID, text)
		if _, err := n.bot.Send(msg); err != nil {
			n.logger.Error("failed to send telegram message", "chat_id", chatID, "error", err)
		}
	}
}

func (n *Notifier) broadcastTrades(text string) { n.broadcastTo(n.groups.Trades, text) }
func (n *Notifier) broadcastAlerts(text string) { n.broadcastTo(n.groups.Alerts, text) }

func (n *Notifier) handleProposed(msg *nats.Msg) {
	_ = msg.Ack()
	var e events.OrderProposedEvent
	if err := json.Unmarshal(msg.Data, &e); err != nil {
		n.logger.Error("failed to unmarshal proposed event", "error", err)
		return
	}

	n.mu.Lock()
	n.proposed = append(n.proposed, e)
	shouldFlush := len(n.proposed) >= batchSize
	n.mu.Unlock()

	if shouldFlush {
		n.flushProposed()
	}
}

func (n *Notifier) handleApproved(msg *nats.Msg) {
	_ = msg.Ack()
	// Approved events are silent — the fill notification is what matters
}

func (n *Notifier) handleDenied(msg *nats.Msg) {
	_ = msg.Ack()
	var e events.OrderDeniedEvent
	if err := json.Unmarshal(msg.Data, &e); err != nil {
		n.logger.Error("failed to unmarshal denied event", "error", err)
		return
	}

	n.mu.Lock()
	n.denied = append(n.denied, e)
	shouldFlush := len(n.denied) >= batchSize
	n.mu.Unlock()

	if shouldFlush {
		n.flushDenied()
	}
}

func (n *Notifier) handleSubmitted(msg *nats.Msg) {
	_ = msg.Ack()
	var e events.OrderSubmittedEvent
	if err := json.Unmarshal(msg.Data, &e); err != nil {
		n.logger.Error("failed to unmarshal submitted event", "error", err)
		return
	}
	n.broadcastTrades(FormatOrderSubmitted(e))
}

func (n *Notifier) handleFill(msg *nats.Msg) {
	_ = msg.Ack()
	var e events.OrderFilledEvent
	if err := json.Unmarshal(msg.Data, &e); err != nil {
		n.logger.Error("failed to unmarshal fill event", "error", err)
		return
	}
	n.broadcastTrades(FormatFillNotification(e))
}

func (n *Notifier) handleKill(msg *nats.Msg) {
	_ = msg.Ack()
	var e events.KillSwitchEvent
	if err := json.Unmarshal(msg.Data, &e); err != nil {
		n.logger.Error("failed to unmarshal kill event", "error", err)
		return
	}
	n.broadcastAlerts(FormatKillSwitchNotification(e))
}
