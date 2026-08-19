package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/rchmdndy/telegram-backlog-bot/internal/config"
	"github.com/rchmdndy/telegram-backlog-bot/internal/scheduler"
	"github.com/rchmdndy/telegram-backlog-bot/internal/store"
	"github.com/rchmdndy/telegram-backlog-bot/internal/telegram"
	_ "time/tzdata"
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "healthcheck":
			os.Exit(healthcheck())
		case "backup":
			os.Exit(backup(os.Args[2:]))
		case "integrity":
			os.Exit(integrity())
		}
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	db, err := store.Open(cfg.DatabasePath)
	if err != nil {
		log.Error("database startup failed", "err", err)
		os.Exit(1)
	}
	defer db.Close()
	api, err := tgbotapi.NewBotAPI(cfg.BotToken)
	if err != nil {
		log.Error("telegram startup failed", "err", err)
		os.Exit(1)
	}
	api.Debug = false
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	var pulse atomic.Int64
	pulse.Store(time.Now().UnixNano())
	heartbeatErr := make(chan error, 1)
	go func() { heartbeatErr <- heartbeat(ctx, cfg.HeartbeatPath, db, &pulse) }()
	log.Info("started", "username", api.Self.UserName)
	bot := telegram.New(api, db, cfg.AuthorizedUserID, cfg.AuthorizedChatID, cfg.RecommendationLimit, cfg.Timezone, log)
	bot.Alive = func() { pulse.Store(time.Now().UnixNano()) }
	s := &scheduler.Scheduler{DB: db, Clock: scheduler.RealClock{}, Sender: bot, Location: cfg.Timezone, Hour: cfg.NotificationHour, Minute: cfg.NotificationMinute, Limit: cfg.RecommendationLimit, Alive: func() { pulse.Store(time.Now().UnixNano()) }}
	errCh := make(chan error, 2)
	go func() { errCh <- bot.Poll(ctx) }()
	go func() { errCh <- s.Run(ctx) }()
	select {
	case err := <-heartbeatErr:
		if err != nil {
			log.Error("heartbeat stopped", "err", err)
			stop()
			os.Exit(1)
		}
	case err := <-errCh:
		if err != nil {
			log.Error("runtime loop stopped", "err", err)
			stop()
			os.Exit(1)
		}
		stop()
	}
}
func heartbeat(ctx context.Context, path string, db *store.Store, pulse *atomic.Int64) error {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	started := time.Now().UTC()
	for {
		if err := writeHeartbeat(path, started); err != nil {
			return err
		}
		if err := db.ReadOnlyIntegrity(ctx); err != nil {
			return err
		}
		if time.Since(time.Unix(0, pulse.Load())) > 2*time.Minute {
			return fmt.Errorf("runtime loop heartbeat is stale")
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}
func writeHeartbeat(path string, started time.Time) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	b, err := json.Marshal(map[string]any{"started_at": started.Format(time.RFC3339Nano), "last_success_at": now})
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
func healthcheck() int {
	path := os.Getenv("HEARTBEAT_PATH")
	if path == "" {
		path = "/data/.heartbeat"
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return 1
	}
	var v struct {
		Last string `json:"last_success_at"`
	}
	if json.Unmarshal(b, &v) != nil {
		return 1
	}
	t, err := time.Parse(time.RFC3339Nano, v.Last)
	if err != nil || time.Since(t) > 90*time.Second {
		return 1
	}
	pathDB := os.Getenv("DATABASE_PATH")
	if pathDB == "" {
		pathDB = "/data/backlog.db"
	}
	db, err := store.Open(pathDB)
	if err != nil {
		return 1
	}
	defer db.Close()
	if err = db.ReadOnlyIntegrity(context.Background()); err != nil {
		return 1
	}
	return 0
}
func integrity() int {
	path := os.Getenv("DATABASE_PATH")
	if path == "" {
		path = "/data/backlog.db"
	}
	db, err := store.OpenReadOnly(path)
	if err != nil {
		return 1
	}
	defer db.Close()
	if err := db.Integrity(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

func backup(args []string) int {
	output := os.Getenv("BACKUP_DIR")
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "--output" {
			output = args[i+1]
		}
	}
	if output == "" {
		output = "/backups"
	}
	path := os.Getenv("DATABASE_PATH")
	if path == "" {
		path = "/data/backlog.db"
	}
	db, err := store.Open(path)
	if err != nil {
		return 1
	}
	defer db.Close()
	if err = db.Backup(context.Background(), output); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}
