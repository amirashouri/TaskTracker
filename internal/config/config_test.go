package config

import (
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testKeyJWT = "JWT_SECRET"

// isolate clears the process variable so a real shell or a stray .env in the
// working tree cannot influence the test, and restores it afterwards.
func isolate(t *testing.T, key string) {
	t.Helper()
	original, had := os.LookupEnv(key)
	os.Unsetenv(key)
	t.Cleanup(func() {
		if had {
			os.Setenv(key, original)
			return
		}
		os.Unsetenv(key)
	})
}

func writeEnvFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write env file: %v", err)
	}
	return path
}

func TestLoadRequiresJWTSecret(t *testing.T) {
	isolate(t, testKeyJWT)
	t.Setenv("ENV_FILE", filepath.Join(t.TempDir(), "absent.env"))

	if _, err := Load(); err == nil {
		t.Fatal("expected an error when JWT_SECRET is unset")
	}
}

func TestLoadRejectsShortJWTSecret(t *testing.T) {
	isolate(t, testKeyJWT)
	t.Setenv(testKeyJWT, "too-short")
	t.Setenv("ENV_FILE", filepath.Join(t.TempDir(), "absent.env"))

	if _, err := Load(); err == nil {
		t.Fatal("expected an error when JWT_SECRET is shorter than 32 bytes")
	}
}

func TestLoadReadsValuesFromEnvFile(t *testing.T) {
	isolate(t, testKeyJWT)
	isolate(t, "DATABASE_URL")

	secret := strings.Repeat("a", 32)
	path := writeEnvFile(t, "# comment\nDATABASE_URL=sqlite:///tmp/from-file.db\n"+testKeyJWT+"="+secret+"\n")
	t.Setenv("ENV_FILE", path)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DatabaseURL != "sqlite:///tmp/from-file.db" {
		t.Errorf("DatabaseURL = %q, want value from .env", cfg.DatabaseURL)
	}
	if cfg.JWTSecret != secret {
		t.Errorf("JWTSecret = %q, want value from .env", cfg.JWTSecret)
	}
}

func TestLoadPrefersHostEnvironmentOverEnvFile(t *testing.T) {
	isolate(t, testKeyJWT)

	hostSecret := strings.Repeat("b", 32)
	path := writeEnvFile(t, testKeyJWT+"="+strings.Repeat("a", 32)+"\n")
	t.Setenv("ENV_FILE", path)
	t.Setenv(testKeyJWT, hostSecret)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.JWTSecret != hostSecret {
		t.Errorf("JWTSecret = %q, want the host environment value to win", cfg.JWTSecret)
	}
}

func TestLoadReadsSignInLimits(t *testing.T) {
	isolate(t, testKeyJWT)
	isolate(t, "SIGNIN_MAX_ATTEMPTS")
	isolate(t, "SIGNIN_WINDOW")
	t.Setenv("ENV_FILE", filepath.Join(t.TempDir(), "absent.env"))
	t.Setenv(testKeyJWT, strings.Repeat("a", 32))
	t.Setenv("SIGNIN_MAX_ATTEMPTS", "5")
	t.Setenv("SIGNIN_WINDOW", "2m")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.SignInMaxAttempts != 5 {
		t.Errorf("SignInMaxAttempts = %d, want 5", cfg.SignInMaxAttempts)
	}
	if cfg.SignInWindow != 2*time.Minute {
		t.Errorf("SignInWindow = %v, want 2m", cfg.SignInWindow)
	}
}

func TestLoadRejectsNegativeSignInAttempts(t *testing.T) {
	isolate(t, testKeyJWT)
	isolate(t, "SIGNIN_MAX_ATTEMPTS")
	t.Setenv("ENV_FILE", filepath.Join(t.TempDir(), "absent.env"))
	t.Setenv(testKeyJWT, strings.Repeat("a", 32))
	t.Setenv("SIGNIN_MAX_ATTEMPTS", "-1")

	if _, err := Load(); err == nil {
		t.Fatal("expected an error when SIGNIN_MAX_ATTEMPTS is negative")
	}
}

// An install that forgets SERVER_BIND must be unreachable rather than exposed.
func TestLoadDefaultsServerBindToLoopback(t *testing.T) {
	isolate(t, testKeyJWT)
	isolate(t, "SERVER_BIND")
	t.Setenv("ENV_FILE", filepath.Join(t.TempDir(), "absent.env"))
	t.Setenv(testKeyJWT, strings.Repeat("a", 32))

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ServerBind != "127.0.0.1" {
		t.Errorf("ServerBind = %q, want 127.0.0.1", cfg.ServerBind)
	}
}

func TestLoadAcceptsIPLiteralServerBind(t *testing.T) {
	for _, bind := range []string{"0.0.0.0", "::1", "192.168.1.10"} {
		t.Run(bind, func(t *testing.T) {
			isolate(t, testKeyJWT)
			isolate(t, "SERVER_BIND")
			t.Setenv("ENV_FILE", filepath.Join(t.TempDir(), "absent.env"))
			t.Setenv(testKeyJWT, strings.Repeat("a", 32))
			t.Setenv("SERVER_BIND", bind)

			cfg, err := Load()
			if err != nil {
				t.Fatalf("Load with SERVER_BIND=%q: %v", bind, err)
			}
			if cfg.ServerBind != bind {
				t.Errorf("ServerBind = %q, want %q", cfg.ServerBind, bind)
			}
		})
	}
}

// A hostname would be resolved to whatever the host's resolver says, which is
// not the address the operator saw. Refuse it instead. An explicitly empty
// value is also an error: it is a missing decision, not a request for the
// default.
func TestLoadRejectsNonLiteralServerBind(t *testing.T) {
	cases := []struct {
		name string
		bind string
	}{
		{"hostname", "localhost"},
		{"empty", ""},
		{"whitespace", "   "},
		{"host and port", "127.0.0.1:8080"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			isolate(t, testKeyJWT)
			isolate(t, "SERVER_BIND")
			t.Setenv("ENV_FILE", filepath.Join(t.TempDir(), "absent.env"))
			t.Setenv(testKeyJWT, strings.Repeat("a", 32))
			t.Setenv("SERVER_BIND", tc.bind)

			_, err := Load()
			if err == nil {
				t.Fatalf("Load with SERVER_BIND=%q succeeded, want an error", tc.bind)
			}
			if !strings.Contains(err.Error(), "SERVER_BIND") {
				t.Errorf("error %q does not name SERVER_BIND", err)
			}
		})
	}
}

// The resolver compares request addresses against these prefixes, so they must
// be parsed and normalized once at startup.
func TestLoadReadsTrustedProxies(t *testing.T) {
	isolate(t, testKeyJWT)
	isolate(t, "TRUSTED_PROXIES")
	t.Setenv("ENV_FILE", filepath.Join(t.TempDir(), "absent.env"))
	t.Setenv(testKeyJWT, strings.Repeat("a", 32))
	t.Setenv("TRUSTED_PROXIES", "127.0.0.1/32, 10.0.0.0/8, 2001:db8::/32")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := []string{"127.0.0.1/32", "10.0.0.0/8", "2001:db8::/32"}
	if len(cfg.TrustedProxies) != len(want) {
		t.Fatalf("TrustedProxies = %v, want %v", cfg.TrustedProxies, want)
	}
	for i, prefix := range cfg.TrustedProxies {
		if got := prefix.String(); got != want[i] {
			t.Errorf("TrustedProxies[%d] = %q, want %q", i, got, want[i])
		}
	}
	if !cfg.TrustedProxies[0].Contains(netip.MustParseAddr("127.0.0.1")) {
		t.Error("127.0.0.1/32 does not contain 127.0.0.1")
	}
	if cfg.TrustedProxies[0].Contains(netip.MustParseAddr("10.0.0.1")) {
		t.Error("127.0.0.1/32 contains an unrelated address")
	}
	if !cfg.TrustedProxies[2].Contains(netip.MustParseAddr("2001:db8::5")) {
		t.Error("2001:db8::/32 does not contain a member address")
	}
}

// The zero value is the safe default: an install that never configures a proxy
// must keep using RemoteAddr.
func TestLoadLeavesTrustedProxiesEmptyByDefault(t *testing.T) {
	isolate(t, testKeyJWT)
	isolate(t, "TRUSTED_PROXIES")
	t.Setenv("ENV_FILE", filepath.Join(t.TempDir(), "absent.env"))
	t.Setenv(testKeyJWT, strings.Repeat("a", 32))

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.TrustedProxies) != 0 {
		t.Errorf("TrustedProxies = %v, want none", cfg.TrustedProxies)
	}
}

func TestLoadRejectsInvalidTrustedProxies(t *testing.T) {
	cases := []struct {
		name  string
		value string
	}{
		{"bare address", "127.0.0.1"},
		{"hostname", "proxy.internal/32"},
		{"garbage", "not-a-cidr"},
		{"trusts every IPv4 address", "0.0.0.0/0"},
		{"trusts every IPv6 address", "::/0"},
		{"mapped everything", "::ffff:0:0/96"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			isolate(t, testKeyJWT)
			isolate(t, "TRUSTED_PROXIES")
			t.Setenv("ENV_FILE", filepath.Join(t.TempDir(), "absent.env"))
			t.Setenv(testKeyJWT, strings.Repeat("a", 32))
			t.Setenv("TRUSTED_PROXIES", tc.value)

			_, err := Load()
			if err == nil {
				t.Fatalf("Load with TRUSTED_PROXIES=%q succeeded, want an error", tc.value)
			}
			if !strings.Contains(err.Error(), "TRUSTED_PROXIES") {
				t.Errorf("error %q does not name TRUSTED_PROXIES", err)
			}
		})
	}
}

// A single-host CIDR is easy to mistype as a bare address. The error has to say
// what to write instead, because a startup failure with no next step is a
// support ticket.
func TestLoadExplainsTheSingleHostCIDR(t *testing.T) {
	isolate(t, testKeyJWT)
	isolate(t, "TRUSTED_PROXIES")
	t.Setenv("ENV_FILE", filepath.Join(t.TempDir(), "absent.env"))
	t.Setenv(testKeyJWT, strings.Repeat("a", 32))
	t.Setenv("TRUSTED_PROXIES", "127.0.0.1")

	_, err := Load()
	if err == nil {
		t.Fatal("Load accepted a bare address")
	}
	if !strings.Contains(err.Error(), "127.0.0.1/32") {
		t.Errorf("error %q does not show the single-host CIDR", err)
	}
}

func TestLoadNormalizesTrustedProxyPrefixes(t *testing.T) {
	isolate(t, testKeyJWT)
	isolate(t, "TRUSTED_PROXIES")
	t.Setenv("ENV_FILE", filepath.Join(t.TempDir(), "absent.env"))
	t.Setenv(testKeyJWT, strings.Repeat("a", 32))
	t.Setenv("TRUSTED_PROXIES", "127.0.0.9/8, ::ffff:192.0.2.99/120")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := []string{"127.0.0.0/8", "192.0.2.0/24"}
	if len(cfg.TrustedProxies) != len(want) {
		t.Fatalf("TrustedProxies = %v, want %v", cfg.TrustedProxies, want)
	}
	for i, prefix := range cfg.TrustedProxies {
		if got := prefix.String(); got != want[i] {
			t.Errorf("TrustedProxies[%d] = %q, want %q", i, got, want[i])
		}
	}
}

// A typo in LOG_LEVEL must stop startup rather than silently logging at info.
func TestLoadRejectsAnUnknownLogLevel(t *testing.T) {
	isolate(t, testKeyJWT)
	isolate(t, "LOG_LEVEL")
	t.Setenv("ENV_FILE", filepath.Join(t.TempDir(), "absent.env"))
	t.Setenv(testKeyJWT, strings.Repeat("a", 32))
	t.Setenv("LOG_LEVEL", "verbose")

	if _, err := Load(); err == nil {
		t.Fatal("expected an error when LOG_LEVEL is not a known level")
	}
}

func TestValidateForCloudflareRejectsOverLimit(t *testing.T) {
	cfg := &Config{
		DatabaseURL:        "sqlite:///data/tasktracker.db",
		JWTSecret:          strings.Repeat("a", 32),
		ServerBind:         "127.0.0.1",
		ServerPort:         "8080",
		LogLevel:           "info",
		AttachmentRoot:     "./data/blobstore",
		AttachmentMaxBytes: CloudflareRequestBodyLimit + 1,
	}
	err := cfg.ValidateForCloudflare()
	if err == nil {
		t.Fatal("expected an error when ATTACHMENT_MAX_BYTES exceeds Cloudflare limit")
	}
	if !strings.Contains(err.Error(), "Cloudflare's 100 MiB") {
		t.Errorf("error %q does not name Cloudflare's limit", err)
	}
}

func TestValidateForCloudflareAcceptsAtLimit(t *testing.T) {
	cfg := &Config{
		DatabaseURL:        "sqlite:///data/tasktracker.db",
		JWTSecret:          strings.Repeat("a", 32),
		ServerBind:         "127.0.0.1",
		ServerPort:         "8080",
		LogLevel:           "info",
		AttachmentRoot:     "./data/blobstore",
		AttachmentMaxBytes: CloudflareRequestBodyLimit,
	}
	if err := cfg.ValidateForCloudflare(); err != nil {
		t.Fatalf("ValidateForCloudflare: %v", err)
	}
}

func TestValidateForCloudflareAcceptsUnderLimit(t *testing.T) {
	cfg := &Config{
		DatabaseURL:        "sqlite:///data/tasktracker.db",
		JWTSecret:          strings.Repeat("a", 32),
		ServerBind:         "127.0.0.1",
		ServerPort:         "8080",
		LogLevel:           "info",
		AttachmentRoot:     "./data/blobstore",
		AttachmentMaxBytes: 25 * 1024 * 1024, // 25 MiB default
	}
	if err := cfg.ValidateForCloudflare(); err != nil {
		t.Fatalf("ValidateForCloudflare: %v", err)
	}
}

func TestSlogLevelMapsTheConfiguredName(t *testing.T) {
	cases := map[string]slog.Level{
		"debug":   slog.LevelDebug,
		"info":    slog.LevelInfo,
		"warn":    slog.LevelWarn,
		"warning": slog.LevelWarn,
		"error":   slog.LevelError,
		"INFO":    slog.LevelInfo,
	}
	for name, want := range cases {
		cfg := &Config{LogLevel: name}
		if got := cfg.SlogLevel(); got != want {
			t.Errorf("SlogLevel(%q) = %v, want %v", name, got, want)
		}
	}
}
