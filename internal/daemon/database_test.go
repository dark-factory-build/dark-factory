//go:build darwin || linux

package daemon

import (
	"context"
	"errors"
	"io"
	"os"

	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

func createTestStore(ctx context.Context, path string, config kernel.FactoryConfig, at kernel.UnixMillis) (*kernel.Store, error) {
	image, err := kernel.NewDatabaseImage(ctx, config, at)
	if err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
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
	return kernel.Open(ctx, path)
}
