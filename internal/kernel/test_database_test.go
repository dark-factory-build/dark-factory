package kernel

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
)

// createTestStore composes the same two production seams the installer will
// own: build a complete image, publish it create-only, then open it. It is a
// test fixture, not a filesystem publication contract.
func createTestStore(ctx context.Context, path string, config FactoryConfig, at UnixMillis) (*Store, error) {
	image, err := NewDatabaseImage(ctx, config, at)
	if err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create test sqlite image: %w", err)
	}
	_, writeErr := file.Write(image)
	if writeErr == nil {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		return nil, errors.Join(writeErr, closeErr)
	}
	if info, err := os.Stat(path); err != nil {
		return nil, err
	} else if info.Size() != int64(len(image)) {
		return nil, io.ErrShortWrite
	}
	return Open(ctx, path)
}
