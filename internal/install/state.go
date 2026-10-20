// Package install owns the tt CLI's install state, its pure plan, and the
// adapter that executes a plan. The plan reads only its arguments; the
// default-path helpers are the only environment-aware code here.
package install

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

// StateVersion is the schema version of the install.json document. It is not
// the server's database schema version.
const StateVersion = 1

// ErrStateNotFound means the customer has not installed an instance yet. It is
// deliberately distinct from a corrupt state file: one is an invitation to
// install, the other is a reason to stop.
var ErrStateNotFound = errors.New("install state not found")

// instanceNamePattern is the Docker Compose project-name shape. The instance
// name becomes `--project-name`, so a space or an uppercase letter is a
// failure the customer would otherwise meet as a Compose error halfway through
// the install.
var instanceNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

// Mode is the database shape the instance runs.
type Mode string

const (
	ModeSQLite   Mode = "sqlite"
	ModePostgres Mode = "postgres"
)

// Provider is how the instance is reached from outside the host.
type Provider string

const (
	ProviderLAN        Provider = "lan"
	ProviderTailscale  Provider = "tailscale"
	ProviderCloudflare Provider = "cloudflare"
)

// State is the install.json document. Secrets live here because the file is
// 0600 and the CLI must be able to repair an install without regenerating them.
type State struct {
	Version          int      `json:"version"`
	InstanceName     string   `json:"instanceName"`
	Mode             Mode     `json:"mode"`
	Provider         Provider `json:"provider"`
	DataDir          string   `json:"dataDir"`
	ComposePath      string   `json:"composePath"`
	ImageTag         string   `json:"imageTag"`
	ServerPort       int      `json:"serverPort"`
	JWTSecret        string   `json:"jwtSecret"`
	DatabasePassword string   `json:"databasePassword,omitempty"`

	// Path is the state file this document lives in. It is not part of the
	// document, so a hand-edited copy cannot move its own location.
	Path string `json:"-"`
}

// Validate refuses a state that would produce a broken or unsafe install. It is
// the same check for a loaded document and a freshly built one.
func (s State) Validate() error {
	if s.Version != StateVersion {
		return fmt.Errorf("install state version %d is not supported (this tt understands %d)", s.Version, StateVersion)
	}
	if !instanceNamePattern.MatchString(s.InstanceName) {
		return fmt.Errorf("install state instance name %q must start with a lowercase letter or digit and contain only lowercase letters, digits, hyphens, and underscores", s.InstanceName)
	}
	switch s.Mode {
	case ModeSQLite, ModePostgres:
	default:
		return fmt.Errorf("install state mode %q is not sqlite or postgres", s.Mode)
	}
	switch s.Provider {
	case ProviderLAN, ProviderTailscale, ProviderCloudflare:
	default:
		return fmt.Errorf("install state provider %q is not lan, tailscale, or cloudflare", s.Provider)
	}
	if strings.TrimSpace(s.DataDir) == "" {
		return errors.New("install state has no data directory")
	}
	if strings.TrimSpace(s.ComposePath) == "" {
		return errors.New("install state has no compose path")
	}
	if strings.TrimSpace(s.ImageTag) == "" {
		return errors.New("install state has no image tag; reinstall with a versioned tt")
	}
	if s.ServerPort <= 0 || s.ServerPort > 65535 {
		return fmt.Errorf("install state port %d is not between 1 and 65535", s.ServerPort)
	}
	if len(s.JWTSecret) < 32 {
		return errors.New("install state has no JWT secret; it cannot repair without one")
	}
	if s.Mode == ModePostgres && s.DatabasePassword == "" {
		return errors.New("install state has no database password for the postgres mode")
	}
	return nil
}

// DefaultStatePath is where install.json lives on this platform. Linux uses
// the system-wide location because the server runs as a service; other
// platforms keep it in the user's config directory.
func DefaultStatePath() (string, error) {
	if runtime.GOOS == "linux" {
		return "/etc/tasktracker/install.json", nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("find home directory: %w", err)
	}
	return filepath.Join(home, ".config", "tasktracker", "install.json"), nil
}

// DefaultDataDir is the directory the CLI owns for the rendered compose file.
// An empty result means the user's home directory could not be found; the
// caller must ask for --data-dir.
func DefaultDataDir() string {
	if runtime.GOOS == "linux" {
		return "/var/lib/tasktracker"
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".local", "share", "tasktracker")
}

// LoadState reads and validates one install document. A missing file is
// ErrStateNotFound; a corrupt or partial file is an error the caller must not
// overwrite.
func LoadState(path string) (State, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return State{}, ErrStateNotFound
	}
	if err != nil {
		return State{}, fmt.Errorf("read install state %s: %w", path, err)
	}

	var state State
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil {
		return State{}, fmt.Errorf("install state %s is not valid: %w", path, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return State{}, fmt.Errorf("install state %s has trailing data", path)
	}

	state.Path = path
	if err := state.Validate(); err != nil {
		return State{}, fmt.Errorf("install state %s: %w", path, err)
	}
	return state, nil
}

// SaveState writes the document atomically. A crash leaves the previous file
// intact, and the 0600 mode keeps the embedded secrets out of other accounts.
func SaveState(state State) error {
	if state.Path == "" {
		return errors.New("install state path is not set")
	}
	if err := state.Validate(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode install state: %w", err)
	}
	data = append(data, '\n')
	return writeFileAtomic(state.Path, data, 0o600)
}

// NewSecret returns 32 random bytes as hex. The server requires at least 32
// bytes of JWT key material, and the same function supplies the database
// password when the postgres mode is chosen.
func NewSecret() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate secret: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}

// writeFileAtomic writes through a temporary file in the destination directory
// and renames it into place. Compose files and state files share it, so both
// are never observed half-written.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}

	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temporary file for %s: %w", path, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return fmt.Errorf("set mode on %s: %w", tmpName, err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close %s: %w", path, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}

	// Best effort: the rename is already durable enough for an operator, and
	// a directory sync failure should not turn a successful write into a
	// failure the caller retries.
	if handle, err := os.Open(dir); err == nil {
		_ = handle.Sync()
		_ = handle.Close()
	}
	return nil
}
