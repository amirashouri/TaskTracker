package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
