// Package attachments serves attachment bytes. The metadata lives in the sync
// engine (an `attachment` entity, member-scoped like the rest of the feed); the
// bytes live here, on disk, and are linked to a row by its id. Keeping the two
// apart is why a 25 MiB photo never has to travel inside the JSON sync payload.
package attachments

import (
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
