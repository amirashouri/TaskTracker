// Package runner is the worker half of agent runs: the `tt agent` config, the
// client for the runner routes, and the loop that claims work and reports it.
//
// Nothing here is imported by the server. The worker lives on the machine that
// holds the code, often not the host the server runs on, so this package exists
// only for `cmd/tt`.
package runner

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ConfigVersion is the schema version of agent.json. It is not the install
// document's version: the two files are independent, and a runner does not
// require an install at all.
const ConfigVersion = 1

// ErrConfigNotFound means this machine has not been paired yet.
var ErrConfigNotFound = errors.New("agent config not found")

// Config is the agent.json document. It holds a bearer secret, which is why the
// file is written 0600 and the token is never printed back in full.
type Config struct {
	Version    int    `json:"version"`
	ServerURL  string `json:"serverUrl"`
	RunnerID   string `json:"runnerId"`
	RunnerName string `json:"runnerName"`
	Token      string `json:"token"`
	Provider   string `json:"provider"`
	Model      string `json:"model"`
	// BaseURL and APIKey are the model endpoint and its key. They stay on this
	// machine: the server registers the provider name and model label, never these.
	BaseURL string `json:"baseUrl,omitempty"`
	APIKey  string `json:"apiKey,omitempty"`
}

// DefaultConfigPath resolves TT_AGENT_CONFIG, or the per-user config directory.
//
// It is deliberately separate from install.DefaultStatePath: a runner lives where
// the code is, and that is usually a developer machine rather than the server
// host, so `tt agent` must work without install.json.
func DefaultConfigPath() (string, error) {
	if path := strings.TrimSpace(os.Getenv("TT_AGENT_CONFIG")); path != "" {
		return path, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("find the config directory: %w", err)
	}
	return filepath.Join(dir, "tasktracker", "agent.json"), nil
}

// LoadConfig reads one document. A missing file is ErrConfigNotFound, which a
// command turns into "run tt agent connect" rather than a parse error.
func LoadConfig(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Config{}, ErrConfigNotFound
	}
	if err != nil {
		return Config{}, err
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse %s: %w", path, err)
	}
	if strings.TrimSpace(cfg.ServerURL) == "" || strings.TrimSpace(cfg.Token) == "" {
		return Config{}, fmt.Errorf("%s is incomplete; run tt agent connect", path)
	}
	cfg.ServerURL = strings.TrimRight(cfg.ServerURL, "/")
	cfg.Provider = strings.TrimSpace(cfg.Provider)
	cfg.Model = strings.TrimSpace(cfg.Model)
	cfg.BaseURL = strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	cfg.APIKey = strings.TrimSpace(cfg.APIKey)
	if cfg.Provider == "" {
		cfg.Provider = ProviderEcho
	}
	return cfg, nil
}

// SaveConfig writes the document 0600 through a temporary file.
//
// The rename is what makes a failed write harmless: a half-written file would
// still be valid JSON often enough to be confusing, and the token is the one
// value this file cannot afford to lose.
func SaveConfig(path string, cfg Config) error {
	cfg.Version = ConfigVersion
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

// RemoveConfig deletes the local credential. A missing file is success: the
// caller's intent was that there should not be one.
func RemoveConfig(path string) error {
	err := os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
