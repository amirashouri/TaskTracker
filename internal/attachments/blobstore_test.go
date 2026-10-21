package attachments

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The root is checked at boot rather than on the first upload. These tests pin
// the two halves of that promise: a missing directory is created, and a path
// that cannot become one is rejected with the path in the message.

func TestEnsureRootCreatesMissingNestedRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "data", "blobstore")

	if err := EnsureRoot(root); err != nil {
		t.Fatalf("EnsureRoot(%q): %v", root, err)
	}

	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		t.Fatalf("root %q was not created as a directory: %v", root, err)
	}

	// The writability probe is a temp file; leaving one behind would be litter
	// in the directory every upload writes to.
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read root: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("EnsureRoot left %d entries in %q, want 0", len(entries), root)
	}
}

func TestEnsureRootRejectsAPathUnderAFile(t *testing.T) {
	// A regular file as the parent fails the same way for every user, including
	// root in CI: mkdir cannot treat it as a directory.
	file := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	root := filepath.Join(file, "blobstore")

	err := EnsureRoot(root)
	if err == nil {
		t.Fatalf("EnsureRoot(%q) accepted a root under a regular file", root)
	}
	if !strings.Contains(err.Error(), root) {
		t.Errorf("error %q does not name the configured root %q", err, root)
	}
	if !strings.Contains(err.Error(), "ATTACHMENT_ROOT") {
		t.Errorf("error %q does not name ATTACHMENT_ROOT, so the operator cannot find the setting", err)
	}
}
