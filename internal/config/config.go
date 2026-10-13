package config

import (
	"bufio"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	DatabaseURL       string
	JWTSecret         string
	JWTAccessTTL      time.Duration
	JWTRefreshTTL     time.Duration
	ServerPort        string
	CORSOrigins       []string
	LogLevel          string
	SignInMaxAttempts int
	SignInWindow      time.Duration
	SyncLogRetention  time.Duration
}

// Load reads configuration from the environment, seeded from the file named by
// ENV_FILE (default ".env"). The file is optional, and never overrides values
// the host already exports, so real environment variables win in production.
func Load() (*Config, error) {
	envFile := getEnv("ENV_FILE", ".env")
	if err := loadEnvFile(envFile); err != nil {
		return nil, fmt.Errorf("load %s: %w", envFile, err)
	}

	cfg := &Config{
		DatabaseURL:       getEnv("DATABASE_URL", "sqlite:///data/tasktracker.db"),
		JWTSecret:         os.Getenv("JWT_SECRET"),
		JWTAccessTTL:      getDurationEnv("JWT_ACCESS_TTL", 1*time.Hour),
		JWTRefreshTTL:     getDurationEnv("JWT_REFRESH_TTL", 720*time.Hour),
		ServerPort:        getEnv("SERVER_PORT", "8080"),
		CORSOrigins:       getCSVEnv("CORS_ORIGINS", "http://localhost:3000,http://localhost:8080"),
		LogLevel:          getEnv("LOG_LEVEL", "info"),
		SignInMaxAttempts: getIntEnv("SIGNIN_MAX_ATTEMPTS", 10),
		SignInWindow:      getDurationEnv("SIGNIN_WINDOW", 15*time.Minute),
		SyncLogRetention:  getDurationEnv("SYNC_LOG_RETENTION", 720*time.Hour),
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *Config) validate() error {
	if c.DatabaseURL == "" {
		return errors.New("DATABASE_URL is required")
	}
	// HS256 signing requires at least 256 bits of key material.
	if len(c.JWTSecret) < 32 {
		return errors.New("JWT_SECRET must be set and at least 32 bytes; see .env.example")
	}
	switch strings.ToLower(strings.TrimSpace(c.LogLevel)) {
	case "debug", "info", "warn", "warning", "error":
	default:
		return fmt.Errorf("LOG_LEVEL %q is not one of debug, info, warn, error", c.LogLevel)
	}
	if c.SignInMaxAttempts < 0 {
		return errors.New("SIGNIN_MAX_ATTEMPTS must not be negative; use 0 to disable")
	}
	if c.SignInMaxAttempts > 0 && c.SignInWindow <= 0 {
		return errors.New("SIGNIN_WINDOW must be positive when SIGNIN_MAX_ATTEMPTS is set")
	}
	if c.SyncLogRetention < 0 {
		return errors.New("SYNC_LOG_RETENTION must not be negative; use 0 to keep the feed forever")
	}
	return nil
}

// SlogLevel maps LOG_LEVEL onto a slog level. Validation has already rejected
// an unknown name, so the default here is only a safety net.
func (c *Config) SlogLevel() slog.Level {
	switch strings.ToLower(strings.TrimSpace(c.LogLevel)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// loadEnvFile parses simple KEY=VALUE lines. Missing file is not an error.
func loadEnvFile(path string) error {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		if _, exists := os.LookupEnv(key); !exists {
			os.Setenv(key, value)
		}
	}
	return scanner.Err()
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func getDurationEnv(key string, defaultValue time.Duration) time.Duration {
	if value := os.Getenv(key); value != "" {
		if d, err := time.ParseDuration(value); err == nil {
			return d
		}
	}
	return defaultValue
}

func getIntEnv(key string, defaultValue int) int {
	if value := os.Getenv(key); value != "" {
		if n, err := strconv.Atoi(value); err == nil {
			return n
		}
	}
	return defaultValue
}

func getCSVEnv(key, defaultValue string) []string {
	value := getEnv(key, defaultValue)
	if value == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			result = append(result, part)
		}
	}
	return result
}
