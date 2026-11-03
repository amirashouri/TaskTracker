package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	maxToolOutput  = 20_000
	maxReadBytes   = 200_000
	maxListEntries = 200
	commandTimeout = 2 * time.Minute
)

// workspace is the only directory the tools may touch. It exists so a model that
// was talked into editing `../../etc/passwd` gets an error instead of a file.
type workspace struct{ root string }

func newWorkspace(dir string) (workspace, error) {
	absolute, err := filepath.Abs(strings.TrimSpace(dir))
	if err != nil {
		return workspace{}, fmt.Errorf("%w: %v", errNoWorkspace, err)
	}
	// The root itself may be reached through a symlink (macOS /var, for one), so
	// resolving it here keeps every later comparison honest.
	if resolved, err := filepath.EvalSymlinks(absolute); err == nil {
		absolute = resolved
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return workspace{}, fmt.Errorf("%w: %v", errNoWorkspace, err)
	}
	if !info.IsDir() {
		return workspace{}, fmt.Errorf("%w: %s is not a directory", errNoWorkspace, absolute)
	}
	return workspace{root: absolute}, nil
}

// resolve maps a model-supplied path into the workspace. A path outside is refused,
// not clamped: writing somewhere else than asked is a worse failure than a tool
// error the model can read and correct.
func (w workspace) resolve(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("path is required")
	}
	joined := raw
	if !filepath.IsAbs(raw) {
		joined = filepath.Join(w.root, raw)
	}
	absolute, err := filepath.Abs(joined)
	if err != nil {
		return "", err
	}
	if err := w.contained(absolute); err != nil {
		return "", err
	}
	return absolute, nil
}

// contained checks the path and, through its deepest existing ancestor, every
// symlink on the way. A link inside the workspace must not become a way out of it.
func (w workspace) contained(absolute string) error {
	probe := absolute
	for {
		resolved, err := filepath.EvalSymlinks(probe)
		if err == nil {
			relative, relErr := filepath.Rel(w.root, resolved)
			if relErr != nil || escapes(relative) {
				return fmt.Errorf("path %q is outside the workspace", absolute)
			}
			return nil
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			return nil
		}
		probe = parent
	}
}

func escapes(relative string) bool {
	return relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

// runnerTools is the whole tool surface. It is deliberately small: read, write,
// list and run, which is enough to implement a task and small enough to review.
func runnerTools() []ToolSpec {
	return []ToolSpec{
		{
			Name:        "list_files",
			Description: "List the entries of a directory inside the workspace. Directories end with a slash.",
			Parameters: object(map[string]any{
				"path": map[string]any{"type": "string", "description": "Directory relative to the workspace; use . for the root."},
			}, "path"),
		},
		{
			Name:        "read_file",
			Description: "Read a UTF-8 text file inside the workspace.",
			Parameters: object(map[string]any{
				"path": map[string]any{"type": "string", "description": "File relative to the workspace."},
			}, "path"),
		},
		{
			Name:        "write_file",
			Description: "Write a file inside the workspace, creating parent directories and replacing any existing content.",
			Parameters: object(map[string]any{
				"path":    map[string]any{"type": "string", "description": "File relative to the workspace."},
				"content": map[string]any{"type": "string", "description": "The complete new content."},
			}, "path", "content"),
		},
		{
			Name:        "run_command",
			Description: "Run a shell command in the workspace root and return its combined output. Use it for builds, tests and search.",
			Parameters: object(map[string]any{
				"command": map[string]any{"type": "string", "description": "Command line to run with the platform shell."},
			}, "command"),
		},
	}
}

func object(properties map[string]any, required ...string) map[string]any {
	return map[string]any{
		"type":                 "object",
		"properties":           properties,
		"required":             required,
		"additionalProperties": false,
	}
}

// executeTool runs one call. The second return value reports a failure the model
// should see and adapt to, which is not the same thing as an error that ends the run.
func executeTool(ctx context.Context, ws workspace, call ToolCall) (string, bool) {
	switch call.Name {
	case "list_files":
		return listFiles(ws, call.Arguments)
	case "read_file":
		return readFile(ws, call.Arguments)
	case "write_file":
		return writeFile(ws, call.Arguments)
	case "run_command":
		return runCommand(ctx, ws, call.Arguments)
	default:
		return fmt.Sprintf("unknown tool %q; available tools are list_files, read_file, write_file, run_command", call.Name), true
	}
}

// decodeArguments rejects unknown fields so a hallucinated parameter is reported
// instead of silently ignored.
func decodeArguments(raw string, target any) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return errors.New("arguments must be a JSON object")
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("arguments: %w", err)
	}
	return nil
}

func listFiles(ws workspace, raw string) (string, bool) {
	var args struct {
		Path string `json:"path"`
	}
	if err := decodeArguments(raw, &args); err != nil {
		return err.Error(), true
	}
	dir, err := ws.resolve(args.Path)
	if err != nil {
		return err.Error(), true
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return err.Error(), true
	}

	var lines []string
	for _, entry := range entries {
		if entry.Name() == ".git" {
			continue
		}
		name := entry.Name()
		if entry.IsDir() {
			name += "/"
		}
		lines = append(lines, name)
		if len(lines) == maxListEntries {
			lines = append(lines, fmt.Sprintf("… more than %d entries", maxListEntries))
			break
		}
	}
	if len(lines) == 0 {
		return "(empty directory)", false
	}
	return strings.Join(lines, "\n"), false
}

func readFile(ws workspace, raw string) (string, bool) {
	var args struct {
		Path string `json:"path"`
	}
	if err := decodeArguments(raw, &args); err != nil {
		return err.Error(), true
	}
	path, err := ws.resolve(args.Path)
	if err != nil {
		return err.Error(), true
	}
	info, err := os.Stat(path)
	if err != nil {
		return err.Error(), true
	}
	if info.IsDir() {
		return fmt.Sprintf("%s is a directory; use list_files", args.Path), true
	}
	if info.Size() > maxReadBytes {
		return fmt.Sprintf("%s is %d bytes; the limit is %d. Use run_command with a targeted tool such as grep or head.", args.Path, info.Size(), maxReadBytes), true
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err.Error(), true
	}
	if len(data) == 0 {
		return "(empty file)", false
	}
	return string(data), false
}

func writeFile(ws workspace, raw string) (string, bool) {
	var args struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := decodeArguments(raw, &args); err != nil {
		return err.Error(), true
	}
	path, err := ws.resolve(args.Path)
	if err != nil {
		return err.Error(), true
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err.Error(), true
	}
	if err := os.WriteFile(path, []byte(args.Content), 0o644); err != nil {
		return err.Error(), true
	}
	return fmt.Sprintf("wrote %d bytes to %s", len(args.Content), args.Path), false
}

func runCommand(ctx context.Context, ws workspace, raw string) (string, bool) {
	var args struct {
		Command string `json:"command"`
	}
	if err := decodeArguments(raw, &args); err != nil {
		return err.Error(), true
	}
	command := strings.TrimSpace(args.Command)
	if command == "" {
		return "command is required", true
	}

	runCtx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()

	// The shell is the point of this tool: a coding agent needs the project's own
	// build and test commands, not a fixed allow-list that would fit none of them.
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(runCtx, "cmd", "/C", command)
	} else {
		cmd = exec.CommandContext(runCtx, "sh", "-c", command)
	}
	cmd.Dir = ws.root

	output, err := cmd.CombinedOutput()
	text := truncate(strings.TrimSpace(string(output)), maxToolOutput)
	if runCtx.Err() == context.DeadlineExceeded {
		return fmt.Sprintf("command timed out after %s\n%s", commandTimeout, text), true
	}
	if err != nil {
		if text == "" {
			text = "(no output)"
		}
		return fmt.Sprintf("command failed: %v\n%s", err, text), true
	}
	if text == "" {
		return "(no output)", false
	}
	return text, false
}
