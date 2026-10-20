package install

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testState(t *testing.T) State {
	t.Helper()

	dir := t.TempDir()
	return State{
		Version:      StateVersion,
		InstanceName: "demo",
		Mode:         ModeSQLite,
		Provider:     ProviderLAN,
		DataDir:      dir,
		ComposePath:  filepath.Join(dir, "docker-compose.yml"),
		ImageTag:     "v1.2.3",
		ServerPort:   8080,
		JWTSecret:    strings.Repeat("a", 64),
		Path:         filepath.Join(dir, "install.json"),
	}
}

func TestSaveAndLoadStateRoundTrips(t *testing.T) {
	state := testState(t)

	if err := SaveState(state); err != nil {
		t.Fatalf("SaveState: %v", err)
	}

	info, err := os.Stat(state.Path)
	if err != nil {
		t.Fatalf("stat state: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("state mode = %o, want 600", got)
	}

	loaded, err := LoadState(state.Path)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if loaded.Path != state.Path {
		t.Errorf("Path = %q, want %q", loaded.Path, state.Path)
	}
	if loaded.InstanceName != state.InstanceName || loaded.Mode != state.Mode ||
		loaded.Provider != state.Provider || loaded.DataDir != state.DataDir ||
		loaded.ImageTag != state.ImageTag || loaded.ServerPort != state.ServerPort ||
		loaded.JWTSecret != state.JWTSecret {
		t.Errorf("loaded state = %+v, want %+v", loaded, state)
	}
}

// The atomic write uses a temporary file in the destination directory. A
// successful save must not leave one behind, or every install accumulates
// secrets in stray files.
func TestSaveStateLeavesNoTemporaryFile(t *testing.T) {
	state := testState(t)
	if err := SaveState(state); err != nil {
		t.Fatalf("SaveState: %v", err)
	}

	entries, err := os.ReadDir(filepath.Dir(state.Path))
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".tmp-") {
			t.Errorf("temporary file %q survived the save", entry.Name())
		}
	}
}

func TestLoadStateMissingIsNotAnErrorToOverwrite(t *testing.T) {
	_, err := LoadState(filepath.Join(t.TempDir(), "absent.json"))
	if !errors.Is(err, ErrStateNotFound) {
		t.Fatalf("LoadState on a missing file = %v, want ErrStateNotFound", err)
	}
}

func TestLoadStateRejectsTruncatedJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "install.json")
	if err := os.WriteFile(path, []byte(`{"version":1,`), 0o600); err != nil {
		t.Fatalf("write state: %v", err)
	}

	if _, err := LoadState(path); err == nil {
		t.Fatal("LoadState accepted a truncated document")
	}
}

func TestLoadStateRejectsAPartialDocument(t *testing.T) {
	path := filepath.Join(t.TempDir(), "install.json")
	if err := os.WriteFile(path, []byte(`{"version":1}`), 0o600); err != nil {
		t.Fatalf("write state: %v", err)
	}

	_, err := LoadState(path)
	if err == nil {
		t.Fatal("LoadState accepted a document with no instance name")
	}
	if !strings.Contains(err.Error(), "instance name") {
		t.Errorf("error %q does not name the missing field", err)
	}
}

// A hand-edited typo must stop the CLI rather than be silently ignored: the
// value the operator thought they set is not the value that would run.
func TestLoadStateRejectsAnUnknownField(t *testing.T) {
	state := testState(t)
	if err := SaveState(state); err != nil {
		t.Fatalf("SaveState: %v", err)
	}

	data, err := os.ReadFile(state.Path)
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	data = bytes.Replace(data, []byte(`"version": 1,`), []byte(`"version": 1, "typo": true,`), 1)
	if err := os.WriteFile(state.Path, data, 0o600); err != nil {
		t.Fatalf("rewrite state: %v", err)
	}

	if _, err := LoadState(state.Path); err == nil {
		t.Fatal("LoadState accepted an unknown field")
	}
}

func TestNewSecretIs32BytesAndUnique(t *testing.T) {
	first, err := NewSecret()
	if err != nil {
		t.Fatalf("NewSecret: %v", err)
	}
	second, err := NewSecret()
	if err != nil {
		t.Fatalf("NewSecret: %v", err)
	}

	raw, err := hex.DecodeString(first)
	if err != nil {
		t.Fatalf("secret is not hex: %v", err)
	}
	if len(raw) != 32 {
		t.Errorf("secret is %d bytes, want 32", len(raw))
	}
	if first == second {
		t.Error("two calls returned the same secret")
	}
}

func TestValidateRejectsAShortJWTSecret(t *testing.T) {
	state := testState(t)
	state.JWTSecret = "short"

	if err := state.Validate(); err == nil {
		t.Fatal("Validate accepted a secret shorter than 32 bytes")
	}
}

func TestValidateRequiresAPostgresPassword(t *testing.T) {
	state := testState(t)
	state.Mode = ModePostgres

	if err := state.Validate(); err == nil {
		t.Fatal("Validate accepted postgres mode without a database password")
	}
}

func TestValidateRejectsAnUnsafeInstanceName(t *testing.T) {
	state := testState(t)
	state.InstanceName = "Demo App"

	if err := state.Validate(); err == nil {
		t.Fatal("Validate accepted an instance name that is not a Compose project name")
	}
}

// A JSON document is not enough to prove the on-disk shape is a state file;
// the field names are part of the contract with a future tt.
func TestSaveStateWritesTheDocumentedKeySet(t *testing.T) {
	state := testState(t)
	if err := SaveState(state); err != nil {
		t.Fatalf("SaveState: %v", err)
	}

	data, err := os.ReadFile(state.Path)
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("decode state: %v", err)
	}
	for _, key := range []string{
		"version", "instanceName", "mode", "provider", "dataDir",
		"composePath", "imageTag", "serverPort", "jwtSecret",
	} {
		if _, ok := decoded[key]; !ok {
			t.Errorf("state document is missing %q", key)
		}
	}
	if _, ok := decoded["path"]; ok {
		t.Error("state document persisted the in-memory Path field")
	}
}
