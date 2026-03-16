package alertbot

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"autonomy-platform/gen/watchdogpb"
	"autonomy-platform/services/dashboard"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Bot handles Telegram command routing and authorization.
type Bot struct {
	api       *tgbotapi.BotAPI
	db        *pgxpool.Pool
	watchdog  watchdogpb.WatchdogClient
	allowed   map[int64]bool
	limiter   *ChatLimiter
	logger    *slog.Logger

	mu            sync.Mutex
	pendingKills  map[int64]time.Time // chatID → confirmation deadline
}

// NewBot creates a Telegram bot with the given allowed chat IDs.
func NewBot(api *tgbotapi.BotAPI, db *pgxpool.Pool, watchdog watchdogpb.WatchdogClient, allowedChatIDs []int64, logger *slog.Logger) *Bot {
	allowed := make(map[int64]bool, len(allowedChatIDs))
	for _, id := range allowedChatIDs {
		allowed[id] = true
	}
	return &Bot{
		api:          api,
		db:           db,
		watchdog:     watchdog,
		allowed:      allowed,
		limiter:      NewChatLimiter(10.0/60.0, 10), // 10 commands/min
		logger:       logger,
		pendingKills: make(map[int64]time.Time),
	}
}

// Run starts the long-polling loop. Blocks until ctx is cancelled.
func (b *Bot) Run(ctx context.Context) {
	u := tgbotapi.NewUpdate(0)
	u.Timeout = 30

	updates := b.api.GetUpdatesChan(u)

	for {
		select {
		case <-ctx.Done():
			b.api.StopReceivingUpdates()
			return
		case update := <-updates:
			if update.Message == nil || !update.Message.IsCommand() {
				continue
			}
			go b.handleCommand(ctx, update.Message)
		}
	}
}

func (b *Bot) handleCommand(ctx context.Context, msg *tgbotapi.Message) {
	chatID := msg.Chat.ID

	if !b.allowed[chatID] {
		b.logger.Warn("unauthorized chat", "chat_id", chatID)
		return
	}

	if !b.limiter.Allow(chatID) {
		b.reply(chatID, "Rate limited. Please wait.")
		return
	}

	b.logger.Info("command received", "chat_id", chatID, "command", msg.Command())

	switch msg.Command() {
	case "status":
		b.cmdStatus(ctx, chatID)
	case "pnl":
		b.cmdPnL(ctx, chatID)
	case "orders":
		b.cmdOrders(ctx, chatID)
	case "kill":
		b.cmdKill(ctx, chatID)
	case "confirm_kill":
		b.cmdConfirmKill(ctx, chatID)
	case "resume":
		b.cmdResume(ctx, chatID)
	case "help":
		b.cmdHelp(chatID)
	default:
		b.reply(chatID, "Unknown command. Send /help for available commands.")
	}
}

func (b *Bot) cmdStatus(ctx context.Context, chatID int64) {
	halts, err := dashboard.QueryActiveHalts(ctx, b.db)
	if err != nil {
		b.reply(chatID, "Failed to query status: "+err.Error())
		return
	}
	mode := "NORMAL"
	if len(halts) > 0 {
		mode = "HALTED"
	}
	b.reply(chatID, FormatStatus(mode, halts))
}

func (b *Bot) cmdPnL(ctx context.Context, chatID int64) {
	stats, err := dashboard.QueryRiskStats(ctx, b.db)
	if err != nil {
		b.reply(chatID, "Failed to query P&L: "+err.Error())
		return
	}
	b.reply(chatID, FormatRiskStats(stats))
}

func (b *Bot) cmdOrders(ctx context.Context, chatID int64) {
	orders, err := dashboard.QueryOpenOrders(ctx, b.db)
	if err != nil {
		b.reply(chatID, "Failed to query orders: "+err.Error())
		return
	}
	b.reply(chatID, FormatOrders(orders))
}

func (b *Bot) cmdKill(ctx context.Context, chatID int64) {
	b.mu.Lock()
	b.pendingKills[chatID] = time.Now().Add(30 * time.Second)
	b.mu.Unlock()
	b.reply(chatID, "Are you sure you want to trigger the kill switch?\nSend /confirm_kill within 30 seconds to confirm.")
}

func (b *Bot) cmdConfirmKill(ctx context.Context, chatID int64) {
	b.mu.Lock()
	deadline, ok := b.pendingKills[chatID]
	delete(b.pendingKills, chatID)
	b.mu.Unlock()

	if !ok || time.Now().After(deadline) {
		b.reply(chatID, "No pending kill confirmation or it expired. Send /kill first.")
		return
	}

	_, err := b.watchdog.TriggerKillSwitch(ctx, &watchdogpb.KillSwitchRequest{
		Level:       watchdogpb.KillSwitchLevel_KILL_LEVEL_CANCEL_ONLY,
		Scope:       "global",
		Reason:      "telegram kill switch",
		TriggeredBy: fmt.Sprintf("telegram:%d", chatID),
	})
	if err != nil {
		b.reply(chatID, "Kill switch failed: "+err.Error())
		return
	}
	b.reply(chatID, "Kill switch activated. All open orders will be cancelled.")
}

func (b *Bot) cmdResume(ctx context.Context, chatID int64) {
	_, err := b.watchdog.ResumeTrading(ctx, &watchdogpb.ResumeTradingRequest{
		Scope: "global",
	})
	if err != nil {
		b.reply(chatID, "Resume failed: "+err.Error())
		return
	}
	b.reply(chatID, "Trading resumed.")
}

func (b *Bot) cmdHelp(chatID int64) {
	b.reply(chatID, `TraderBot Commands:
/status — System status and active halts
/pnl — Daily P&L report
/orders — Open orders
/kill — Trigger kill switch (requires confirmation)
/resume — Resume trading after halt
/help — This message`)
}

func (b *Bot) reply(chatID int64, text string) {
	msg := tgbotapi.NewMessage(chatID, text)
	if _, err := b.api.Send(msg); err != nil {
		b.logger.Error("failed to send reply", "chat_id", chatID, "error", err)
	}
}
