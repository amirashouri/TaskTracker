package config

import (
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
