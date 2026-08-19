package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

type Config struct {
	BotToken            string
	AuthorizedUserID    int64
	AuthorizedChatID    int64
	DatabasePath        string
	Timezone            *time.Location
	NotificationHour    int
	NotificationMinute  int
	RecommendationLimit int
	BackupDir           string
	HeartbeatPath       string
}

func Load() (Config, error) {
	var c Config
	c.BotToken = os.Getenv("TELEGRAM_BOT_TOKEN")
	if c.BotToken == "" {
		return c, fmt.Errorf("TELEGRAM_BOT_TOKEN is required")
	}
	var err error
	if c.AuthorizedUserID, err = requiredInt64("TELEGRAM_AUTHORIZED_USER_ID"); err != nil {
		return c, err
	}
	if c.AuthorizedChatID, err = requiredInt64("TELEGRAM_AUTHORIZED_CHAT_ID"); err != nil {
		return c, err
	}
	c.DatabasePath = getenv("DATABASE_PATH", "/data/backlog.db")
	zone := getenv("TIMEZONE", "Asia/Jakarta")
	c.Timezone, err = time.LoadLocation(zone)
	if err != nil {
		return c, fmt.Errorf("TIMEZONE: %w", err)
	}
	c.NotificationHour, err = intEnv("NOTIFICATION_HOUR", 6, 0, 23)
	if err != nil {
		return c, err
	}
	c.NotificationMinute, err = intEnv("NOTIFICATION_MINUTE", 0, 0, 59)
	if err != nil {
		return c, err
	}
	c.RecommendationLimit, err = intEnv("RECOMMENDATION_LIMIT", 10, 1, 10)
	if err != nil {
		return c, err
	}
	c.BackupDir = getenv("BACKUP_DIR", "/backups")
	c.HeartbeatPath = getenv("HEARTBEAT_PATH", "/data/.heartbeat")
	return c, nil
}

func requiredInt64(name string) (int64, error) {
	value := os.Getenv(name)
	if value == "" {
		return 0, fmt.Errorf("%s is required", name)
	}
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s must be a signed 64-bit integer: %w", name, err)
	}
	return n, nil
}
func intEnv(name string, fallback, min, max int) (int, error) {
	v := getenv(name, strconv.Itoa(fallback))
	n, err := strconv.Atoi(v)
	if err != nil || n < min || n > max {
		return 0, fmt.Errorf("%s must be an integer from %d through %d", name, min, max)
	}
	return n, nil
}
func getenv(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}
