// Package platform holds infrastructure shared by every binary: configuration,
// database access, migrations and cryptography.
package platform

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Env             string // dev | prod
	HTTPAddr        string
	DatabaseURL     string
	MigrateURL      string // owner role; used only by `chador migrate`
	AutoMigrate     bool   // run migrations on `serve` start (dev). Off when a separate migrate step exists.
	AppDBRole       string // unprivileged runtime role that receives grants after migrating
	RedisURL        string
	EncKey          []byte // AES-256 key for personal data
	IdxKey          []byte // HMAC key for blind indexes
	AccessTTL       time.Duration
	RefreshTTL      time.Duration
	UndoWindow      time.Duration
	AllowUnverified bool
	InquiryMode     string // mock | http
	InquiryURL      string
	InquiryToken    string
	SMSMode         string // mock | http
	SMSURL          string
	SMSToken        string
	ReminderAfter   time.Duration
	MaxReminders    int
	RetentionDays   int
	TrustProxy      bool
	Location        *time.Location
}

func (c Config) IsProd() bool { return c.Env == "prod" }

// secret reads NAME, or the file named by NAME_FILE (Docker secrets).
func secret(name string) (string, error) {
	if p := os.Getenv(name + "_FILE"); p != "" {
		b, err := os.ReadFile(p)
		if err != nil {
			return "", fmt.Errorf("%s_FILE: %w", name, err)
		}
		return strings.TrimSpace(string(b)), nil
	}
	return os.Getenv(name), nil
}

func env(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

func envDur(name string, def time.Duration) (time.Duration, error) {
	v := os.Getenv(name)
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", name, err)
	}
	return d, nil
}

func envInt(name string, def int) (int, error) {
	v := os.Getenv(name)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", name, err)
	}
	return n, nil
}

func key32(name string) ([]byte, error) {
	s, err := secret(name)
	if err != nil {
		return nil, err
	}
	if s == "" {
		return nil, fmt.Errorf("%s is required", name)
	}
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil || len(b) != 32 {
		return nil, fmt.Errorf("%s must be 32 random bytes, base64-encoded", name)
	}
	return b, nil
}

func LoadConfig() (Config, error) {
	var c Config
	var err error
	c.Env = env("APP_ENV", "dev")
	c.HTTPAddr = env("HTTP_ADDR", ":8080")
	if c.DatabaseURL, err = secret("DATABASE_URL"); err != nil {
		return c, err
	}
	if c.MigrateURL, err = secret("MIGRATE_DATABASE_URL"); err != nil {
		return c, err
	}
	c.AutoMigrate = env("AUTO_MIGRATE", "true") == "true"
	c.AppDBRole = os.Getenv("APP_DB_ROLE")
	if c.RedisURL, err = secret("REDIS_URL"); err != nil {
		return c, err
	}
	if c.RedisURL == "" {
		c.RedisURL = "redis://localhost:6379/0"
	}
	if c.EncKey, err = key32("PII_ENC_KEY"); err != nil {
		return c, err
	}
	if c.IdxKey, err = key32("PII_IDX_KEY"); err != nil {
		return c, err
	}
	if string(c.EncKey) == string(c.IdxKey) {
		return c, errors.New("PII_ENC_KEY and PII_IDX_KEY must differ")
	}
	if c.AccessTTL, err = envDur("ACCESS_TTL", 15*time.Minute); err != nil {
		return c, err
	}
	if c.RefreshTTL, err = envDur("REFRESH_TTL", 12*time.Hour); err != nil {
		return c, err
	}
	if c.UndoWindow, err = envDur("UNDO_WINDOW", 3*time.Minute); err != nil {
		return c, err
	}
	if c.ReminderAfter, err = envDur("REMINDER_AFTER", 6*time.Hour); err != nil {
		return c, err
	}
	if c.MaxReminders, err = envInt("MAX_REMINDERS", 2); err != nil {
		return c, err
	}
	if c.RetentionDays, err = envInt("RETENTION_DAYS", 90); err != nil {
		return c, err
	}
	c.AllowUnverified = env("ALLOW_UNVERIFIED", "false") == "true"
	c.InquiryMode = env("INQUIRY_MODE", "mock")
	c.InquiryURL = os.Getenv("INQUIRY_URL")
	if c.InquiryToken, err = secret("INQUIRY_TOKEN"); err != nil {
		return c, err
	}
	c.SMSMode = env("SMS_MODE", "mock")
	c.SMSURL = os.Getenv("SMS_URL")
	if c.SMSToken, err = secret("SMS_TOKEN"); err != nil {
		return c, err
	}
	c.TrustProxy = env("TRUST_PROXY", "true") == "true"
	if c.Location, err = time.LoadLocation(env("APP_TZ", "Asia/Tehran")); err != nil {
		return c, err
	}
	if c.IsProd() && (c.InquiryMode == "mock" || c.SMSMode == "mock") {
		return c, errors.New("mock inquiry/SMS providers are not allowed when APP_ENV=prod")
	}
	return c, nil
}
