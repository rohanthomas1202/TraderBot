package alertbot

import (
	"encoding/json"
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

// Notifier subscribes to NATS events and pushes Telegram notifications.
type Notifier struct {
	bot    *tgbotapi.BotAPI
	sub    *events.Subscriber
	groups ChatGroups
	logger *slog.Logger

	mu        sync.Mutex
	lastSent  map[string]time.Time // event type → last send time
	cooldowns map[string]time.Duration
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
		lastSent: make(map[string]time.Time),
		cooldowns: map[string]time.Duration{
			"order.proposed":  0, // immediate
			"order.approved":  0,
			"order.denied":    0,
			"order.submitted": 0,
			"order.filled":    0,
			"kill.activated":  0,
		},
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
	return nil
}

func (n *Notifier) throttled(eventType string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()

	cooldown, ok := n.cooldowns[eventType]
	if !ok {
		cooldown = 5 * time.Second
	}
	if cooldown == 0 {
		return false
	}

	last, ok := n.lastSent[eventType]
	if ok && time.Since(last) < cooldown {
		return true
	}
	n.lastSent[eventType] = time.Now()
	return false
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
	if n.throttled("order.proposed") {
		return
	}
	var e events.OrderProposedEvent
	if err := json.Unmarshal(msg.Data, &e); err != nil {
		n.logger.Error("failed to unmarshal proposed event", "error", err)
		return
	}
	n.broadcastTrades(FormatOrderProposed(e))
}

func (n *Notifier) handleApproved(msg *nats.Msg) {
	_ = msg.Ack()
	if n.throttled("order.approved") {
		return
	}
	var e events.OrderApprovedEvent
	if err := json.Unmarshal(msg.Data, &e); err != nil {
		n.logger.Error("failed to unmarshal approved event", "error", err)
		return
	}
	n.broadcastTrades(FormatOrderApproved(e))
}

func (n *Notifier) handleDenied(msg *nats.Msg) {
	_ = msg.Ack()
	if n.throttled("order.denied") {
		return
	}
	var e events.OrderDeniedEvent
	if err := json.Unmarshal(msg.Data, &e); err != nil {
		n.logger.Error("failed to unmarshal denied event", "error", err)
		return
	}
	n.broadcastAlerts(FormatOrderDenied(e))
}

func (n *Notifier) handleSubmitted(msg *nats.Msg) {
	_ = msg.Ack()
	if n.throttled("order.submitted") {
		return
	}
	var e events.OrderSubmittedEvent
	if err := json.Unmarshal(msg.Data, &e); err != nil {
		n.logger.Error("failed to unmarshal submitted event", "error", err)
		return
	}
	n.broadcastTrades(FormatOrderSubmitted(e))
}

func (n *Notifier) handleFill(msg *nats.Msg) {
	_ = msg.Ack()
	if n.throttled("order.filled") {
		return
	}
	var e events.OrderFilledEvent
	if err := json.Unmarshal(msg.Data, &e); err != nil {
		n.logger.Error("failed to unmarshal fill event", "error", err)
		return
	}
	n.broadcastTrades(FormatFillNotification(e))
}

func (n *Notifier) handleKill(msg *nats.Msg) {
	_ = msg.Ack()
	if n.throttled("kill.activated") {
		return
	}
	var e events.KillSwitchEvent
	if err := json.Unmarshal(msg.Data, &e); err != nil {
		n.logger.Error("failed to unmarshal kill event", "error", err)
		return
	}
	n.broadcastAlerts(FormatKillSwitchNotification(e))
}
