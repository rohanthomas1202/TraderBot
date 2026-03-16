package main

import (
	"context"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"autonomy-platform/gen/watchdogpb"
	"autonomy-platform/internal/events"
	"autonomy-platform/internal/health"
	"autonomy-platform/internal/logging"
	"autonomy-platform/services/alertbot"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func main() {
	logger := logging.SetupLogger("alertbot")

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Telegram bot
	token := os.Getenv("TELEGRAM_BOT_TOKEN")
	if token == "" {
		logger.Error("TELEGRAM_BOT_TOKEN is required")
		os.Exit(1)
	}

	bot, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		logger.Error("failed to create telegram bot", "error", err)
		os.Exit(1)
	}
	logger.Info("telegram bot authorized", "username", bot.Self.UserName)

	// Parse allowed chat IDs
	chatIDs := parseChatIDs(os.Getenv("TELEGRAM_ALLOWED_CHAT_IDS"))
	if len(chatIDs) == 0 {
		logger.Error("TELEGRAM_ALLOWED_CHAT_IDS is required (comma-separated)")
		os.Exit(1)
	}

	// Connect to Postgres
	dbPool, err := pgxpool.New(ctx, envOrDefault("POSTGRES_URL", "postgres://trader:localdev@localhost:5432/autonomy?sslmode=disable"))
	if err != nil {
		logger.Error("failed to connect to postgres", "error", err)
		os.Exit(1)
	}
	defer dbPool.Close()

	// Connect to NATS
	nc, err := nats.Connect(envOrDefault("NATS_URL", "nats://localhost:4222"))
	if err != nil {
		logger.Error("failed to connect to nats", "error", err)
		os.Exit(1)
	}
	defer nc.Close()

	subscriber, err := events.NewSubscriber(nc)
	if err != nil {
		logger.Error("failed to create subscriber", "error", err)
		os.Exit(1)
	}

	// Connect to Watchdog gRPC
	watchdogAddr := envOrDefault("WATCHDOG_ADDR", "localhost:50055")
	watchdogConn, err := grpc.NewClient(watchdogAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		logger.Error("failed to connect to watchdog", "error", err)
		os.Exit(1)
	}
	defer watchdogConn.Close()
	watchdogClient := watchdogpb.NewWatchdogClient(watchdogConn)

	// Parse optional group chat IDs (fall back to default chatIDs if unset)
	groups := alertbot.ChatGroups{
		Trades: parseChatIDs(os.Getenv("TELEGRAM_TRADES_CHAT_ID")),
		Alerts: parseChatIDs(os.Getenv("TELEGRAM_ALERTS_CHAT_ID")),
	}

	// Start notifier (NATS → Telegram push)
	notifier := alertbot.NewNotifier(bot, subscriber, chatIDs, groups, logger)
	if err := notifier.Start(); err != nil {
		logger.Error("failed to start notifier", "error", err)
		os.Exit(1)
	}

	// Health endpoint
	health.New("50082", "alertbot").Start()

	// Start command bot (blocking)
	tgBot := alertbot.NewBot(bot, dbPool, watchdogClient, chatIDs, logger)
	logger.Info("alertbot starting", "allowed_chats", chatIDs, "watchdog", watchdogAddr)
	tgBot.Run(ctx)

	logger.Info("alertbot shutting down")
}

func parseChatIDs(s string) []int64 {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	ids := make([]int64, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		id, err := strconv.ParseInt(p, 10, 64)
		if err != nil {
			continue
		}
		ids = append(ids, id)
	}
	return ids
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
