package runner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkspaceRefusesPathsOutsideTheRoot(t *testing.T) {
	ws, err := newWorkspace(t.TempDir())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	outside := filepath.Join(t.TempDir(), "outside.txt")

	cases := map[string]string{
		"parent":   "../escape.txt",
		"deeper":   "sub/../../escape.txt",
		"absolute": outside,
	}
	for name, raw := range cases {
		if _, err := ws.resolve(raw); err == nil {
			t.Errorf("%s: resolve(%q) succeeded, want a refusal", name, raw)
		}
	}
}

func TestWorkspaceResolvesPathsInsideTheRoot(t *testing.T) {
	ws, err := newWorkspace(t.TempDir())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}

	got, err := ws.resolve("sub/file.txt")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if want := filepath.Join(ws.root, "sub", "file.txt"); got != want {
		t.Errorf("resolve = %q, want %q", got, want)
	}
}

func TestToolsWriteReadAndList(t *testing.T) {
	ws, err := newWorkspace(t.TempDir())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	ctx := context.Background()

	if output, failed := executeTool(ctx, ws, ToolCall{
		Name:      "write_file",
		Arguments: `{"path":"src/main.go","content":"package main\n"}`,
	}); failed {
		t.Fatalf("write_file failed: %s", output)
	}

	read, failed := executeTool(ctx, ws, ToolCall{Name: "read_file", Arguments: `{"path":"src/main.go"}`})
	if failed || !strings.Contains(read, "package main") {
		t.Errorf("read_file = %q (failed %v), want the written content", read, failed)
	}

	list, failed := executeTool(ctx, ws, ToolCall{Name: "list_files", Arguments: `{"path":"src"}`})
	if failed || !strings.Contains(list, "main.go") {
		t.Errorf("list_files = %q (failed %v), want main.go", list, failed)
	}
}

func TestRunCommandReportsOutputAndFailure(t *testing.T) {
	ws, err := newWorkspace(t.TempDir())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	ctx := context.Background()

	output, failed := executeTool(ctx, ws, ToolCall{Name: "run_command", Arguments: `{"command":"echo hello"}`})
	if failed || !strings.Contains(output, "hello") {
		t.Errorf("run_command = %q (failed %v), want hello", output, failed)
	}

	output, failed = executeTool(ctx, ws, ToolCall{Name: "run_command", Arguments: `{"command":"exit 3"}`})
	if !failed {
		t.Errorf("a failing command was reported as success: %q", output)
	}
}

func TestToolArgumentsAndNamesAreStrict(t *testing.T) {
	ws, err := newWorkspace(t.TempDir())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	ctx := context.Background()

	output, failed := executeTool(ctx, ws, ToolCall{Name: "read_file", Arguments: `{"file":"x"}`})
	if !failed || !strings.Contains(output, "file") {
		t.Errorf("unknown argument = %q (failed %v), want it refused", output, failed)
	}

	output, failed = executeTool(ctx, ws, ToolCall{Name: "teleport", Arguments: `{}`})
	if !failed || !strings.Contains(output, "unknown tool") {
		t.Errorf("unknown tool = %q (failed %v), want a refusal naming the tool", output, failed)
	}
}

// A symlink inside the workspace is the escape a plain prefix check misses.
func TestWorkspaceRefusesASymlinkOutOfTheRoot(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	ws, err := newWorkspace(root)
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	if _, err := ws.resolve("link/secret.txt"); err == nil {
		t.Error("a symlink escaped the workspace")
	}
}
