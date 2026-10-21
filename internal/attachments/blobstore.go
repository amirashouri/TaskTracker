// Package attachments serves attachment bytes. The metadata lives in the sync
// engine (an `attachment` entity, member-scoped like the rest of the feed); the
// bytes live here, on disk, and are linked to a row by its id. Keeping the two
// apart is why a 25 MiB photo never has to travel inside the JSON sync payload.
package attachments

import (
	"fmt"
	"os"
	"path/filepath"
)

// BlobStore writes, opens and removes attachment bytes under a root directory.
// Files are keyed flat by attachment id — the path formula is shared with the
// sync engine's removeAttachmentBlob, so a delete removes exactly the file it wrote.
type BlobStore struct {
	Root string
}

func (b *BlobStore) path(attachmentID string) string {
	return filepath.Join(b.Root, attachmentID+".blob")
}

// EnsureRoot creates the root if needed and proves it is writable.
//
// This runs at startup because the failure it catches is otherwise invisible:
// the root used to default to a path relative to the working directory, which
// the image (/app, root-owned) and the systemd unit (/opt/tasktracker, read-only
// under ProtectSystem=strict) do not let the service write. Write would then
// fail on every upload, and the handler could only answer 500 with no clue why.
func EnsureRoot(root string) error {
	if err := os.MkdirAll(root, 0o750); err != nil {
		return fmt.Errorf("ATTACHMENT_ROOT %s is not a writable directory: %w", root, err)
	}
	probe, err := os.CreateTemp(root, ".write-probe-*")
	if err != nil {
		return fmt.Errorf("ATTACHMENT_ROOT %s is not writable: %w", root, err)
	}
	name := probe.Name()
	if err := probe.Close(); err != nil {
		return fmt.Errorf("close probe in ATTACHMENT_ROOT %s: %w", root, err)
	}
	if err := os.Remove(name); err != nil {
		return fmt.Errorf("remove probe in ATTACHMENT_ROOT %s: %w", root, err)
	}
	return nil
}

// Write creates the root if needed and stores the bytes. A file per attachment
// (never a column) is what keeps the binary out of the change feed.
func (b *BlobStore) Write(attachmentID string, data []byte) error {
	if err := os.MkdirAll(b.Root, 0o750); err != nil {
		return err
	}
	return os.WriteFile(b.path(attachmentID), data, 0o600)
}

// Open returns the bytes for reading. The caller closes it.
func (b *BlobStore) Open(attachmentID string) (*os.File, error) {
	return os.Open(b.path(attachmentID))
}

// Remove deletes the bytes, treating a missing file as success (it was never
// uploaded, or already removed).
func (b *BlobStore) Remove(attachmentID string) error {
	err := os.Remove(b.path(attachmentID))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
