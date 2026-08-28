//go:build darwin

package e2e_test

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

func createTestStore(ctx context.Context, path string, config kernel.FactoryConfig, at kernel.UnixMillis) (*kernel.Store, error) {
	parent, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(parent, 0o700); err != nil {
		return nil, err
	}
	path = filepath.Join(parent, filepath.Base(path))
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
