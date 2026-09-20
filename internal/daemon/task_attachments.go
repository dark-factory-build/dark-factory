package daemon

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

// Each attempt gets a fresh copy; the immutable database bytes survive retries.
func materializeTaskAttachments(home string, attachments []kernel.TaskAttachment) error {
	if len(attachments) == 0 {
		return nil
	}
	if _, err := kernel.TaskAttachmentInstruction("", attachments); err != nil {
		return err
	}
	root, err := os.OpenRoot(home)
	if err != nil {
		return err
	}
	defer root.Close()
	if err := root.Mkdir("task-attachments", 0o700); err != nil {
		return err
	}
	for i, item := range attachments {
		name := filepath.Join("task-attachments", kernel.AttachmentFileName(i, item.Name))
		file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return err
		}
		_, writeErr := file.Write(item.Data)
		closeErr := file.Close()
		if writeErr != nil {
			return fmt.Errorf("write task attachment: %w", writeErr)
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}
