package config

import (
	"bufio"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	DatabaseURL        string
	JWTSecret          string
	JWTAccessTTL       time.Duration
	JWTRefreshTTL      time.Duration
	ServerBind         string
	ServerPort         string
	TrustedProxies     []netip.Prefix
	CORSOrigins        []string
	LogLevel           string
	SignInMaxAttempts  int
	SignInWindow       time.Duration
	SyncLogRetention   time.Duration
	AttachmentRoot     string
	AttachmentMaxBytes int64
}

// Load reads configuration from the environment, seeded from the file named by
// ENV_FILE (default ".env"). The file is optional, and never overrides values
// the host already exports, so real environment variables win in production.
func Load() (*Config, error) {
	envFile := getEnv("ENV_FILE", ".env")
	if err := loadEnvFile(envFile); err != nil {
		return nil, fmt.Errorf("load %s: %w", envFile, err)
	}

	trustedProxies, err := parseTrustedProxies(getCSVEnv("TRUSTED_PROXIES", ""))
	if err != nil {
		return nil, err
	}

	cfg := &Config{
		DatabaseURL:        getEnv("DATABASE_URL", "sqlite:///data/tasktracker.db"),
		JWTSecret:          os.Getenv("JWT_SECRET"),
		JWTAccessTTL:       getDurationEnv("JWT_ACCESS_TTL", 1*time.Hour),
		JWTRefreshTTL:      getDurationEnv("JWT_REFRESH_TTL", 720*time.Hour),
		ServerBind:         serverBind(),
		ServerPort:         getEnv("SERVER_PORT", "8080"),
		TrustedProxies:     trustedProxies,
		CORSOrigins:        getCSVEnv("CORS_ORIGINS", "http://localhost:3000,http://localhost:8080"),
		LogLevel:           getEnv("LOG_LEVEL", "info"),
		SignInMaxAttempts:  getIntEnv("SIGNIN_MAX_ATTEMPTS", 10),
		SignInWindow:       getDurationEnv("SIGNIN_WINDOW", 15*time.Minute),
		SyncLogRetention:   getDurationEnv("SYNC_LOG_RETENTION", 720*time.Hour),
		AttachmentRoot:     getEnv("ATTACHMENT_ROOT", "./data/blobstore"),
		AttachmentMaxBytes: int64(getIntEnv("ATTACHMENT_MAX_BYTES", 25*1024*1024)),
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
	if c.ServerBind == "" {
		return errors.New("SERVER_BIND is required; set it to an IP literal such as 127.0.0.1 or 0.0.0.0")
	}
	if net.ParseIP(c.ServerBind) == nil {
		return fmt.Errorf("SERVER_BIND %q is not an IP literal; hostnames are not resolved", c.ServerBind)
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
	if c.AttachmentRoot == "" {
		return errors.New("ATTACHMENT_ROOT is required; set it to a directory the server can write")
	}
	if c.AttachmentMaxBytes <= 0 {
		return errors.New("ATTACHMENT_MAX_BYTES must be positive; it is the largest attachment the server accepts")
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

// serverBind defaults to loopback: an install that forgets the variable is
// unreachable rather than exposed. An explicitly empty value stays empty so
// validate rejects it instead of silently falling back to the default.
func serverBind() string {
	if value, ok := os.LookupEnv("SERVER_BIND"); ok {
		return strings.TrimSpace(value)
	}
	return "127.0.0.1"
}

// parseTrustedProxies turns TRUSTED_PROXIES into the prefixes the auth resolver
// checks once per request. Empty means trust no peer, which is the safe
// default: a forwarding header from an unconfigured peer is ignored.
func parseTrustedProxies(values []string) ([]netip.Prefix, error) {
	prefixes := make([]netip.Prefix, 0, len(values))
	for _, value := range values {
		prefix, err := netip.ParsePrefix(value)
		if err != nil {
			return nil, fmt.Errorf(
				"TRUSTED_PROXIES entry %q is not an IP prefix (use e.g. 127.0.0.1/32 for one host): %w",
				value, err,
			)
		}
		prefix = prefix.Masked()
		if prefix.Addr().Is4In6() {
			if prefix.Bits() < 96 {
				return nil, fmt.Errorf(
					"TRUSTED_PROXIES entry %q is an IPv4-mapped IPv6 prefix broader than /96; use an IPv4 CIDR",
					value,
				)
			}
			prefix = netip.PrefixFrom(prefix.Addr().Unmap(), prefix.Bits()-96)
		}
		if prefix.Bits() == 0 {
			return nil, fmt.Errorf(
				"TRUSTED_PROXIES entry %q trusts every address, which makes forwarded headers forgeable; list the proxy's own prefix",
				value,
			)
		}
		prefixes = append(prefixes, prefix)
	}
	return prefixes, nil
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
