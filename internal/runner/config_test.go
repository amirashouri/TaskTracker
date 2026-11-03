package runner

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestConfigRoundTripIsPrivateAndNormalised(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "agent.json")
	cfg := Config{
		ServerURL:  "http://192.168.1.20:8080/",
		RunnerID:   "runner-1",
		RunnerName: "box",
		Token:      "ttag_secret",
		Provider:   "echo",
		Model:      "llama3.1",
	}
	if err := SaveConfig(path, cfg); err != nil {
		t.Fatalf("save: %v", err)
	}

	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat: %v", err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Errorf("mode = %o, want 0600: this file holds a bearer secret", got)
		}
	}

	loaded, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.ServerURL != "http://192.168.1.20:8080" {
		t.Errorf("ServerURL = %q, want the trailing slash trimmed", loaded.ServerURL)
	}
	if loaded.Version != ConfigVersion {
		t.Errorf("Version = %d, want %d", loaded.Version, ConfigVersion)
	}
	if loaded.Token != "ttag_secret" || loaded.RunnerID != "runner-1" || loaded.Model != "llama3.1" {
		t.Errorf("loaded = %+v, want the saved values", loaded)
	}
}

func TestLoadConfigSeparatesMissingFromIncomplete(t *testing.T) {
	dir := t.TempDir()

	if _, err := LoadConfig(filepath.Join(dir, "absent.json")); !errors.Is(err, ErrConfigNotFound) {
		t.Errorf("missing file = %v, want ErrConfigNotFound", err)
	}

	path := filepath.Join(dir, "incomplete.json")
	if err := os.WriteFile(path, []byte(`{"serverUrl":"http://server"}`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := LoadConfig(path); err == nil {
		t.Error("a config without a token was accepted")
	}
}

func TestDefaultConfigPathHonoursTheEnvironment(t *testing.T) {
	want := filepath.Join(t.TempDir(), "custom-agent.json")
	t.Setenv("TT_AGENT_CONFIG", want)

	got, err := DefaultConfigPath()
	if err != nil {
		t.Fatalf("DefaultConfigPath: %v", err)
	}
	if got != want {
		t.Errorf("path = %q, want %q", got, want)
	}
}

func TestRemoveConfigIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.json")
	if err := SaveConfig(path, Config{ServerURL: "http://server", Token: "ttag_x"}); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := RemoveConfig(path); err != nil {
		t.Fatalf("first remove: %v", err)
	}
	if err := RemoveConfig(path); err != nil {
		t.Errorf("second remove = %v, want success", err)
	}
}
