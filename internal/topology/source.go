package topology

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// Source records the explicitly configured integrated target, not checkout HEAD.
// Prefix namespaces repositories in projects containing more than one checkout.
type Source struct {
	RepositoryID string `json:"repository_id"`
	Prefix       string `json:"prefix"`
	Kind         string `json:"kind"`
	TargetRef    string `json:"target_ref"`
	Revision     string `json:"revision"`
	ObservedAt   int64  `json:"observed_at"`
	Reason       string `json:"reason,omitempty"`
}
type File struct {
	Path  string `json:"path"`
	Kind  string `json:"kind"`
	Bytes int64  `json:"bytes"`
}

// BuildArchive reuses the normal scanner on ordinary files from one immutable
// Git archive. Links and submodules cannot escape or contribute invented files.
func BuildArchive(ctx context.Context, archive []byte, project, revision string) (Snapshot, error) {
	root, err := os.MkdirTemp("", "dark-factory-topology-")
	if err != nil {
		return Snapshot{}, err
	}
	defer os.RemoveAll(root)
	reader := tar.NewReader(bytes.NewReader(archive))
	files, size := 0, int64(0)
	for {
		if err := ctx.Err(); err != nil {
			return Snapshot{}, err
		}
		item, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return Snapshot{}, err
		}
		if item.Typeflag != tar.TypeReg {
			continue
		}
		name := item.Name
		if name == "." || path.Clean(name) != name || path.IsAbs(name) || strings.HasPrefix(name, "../") {
			return Snapshot{}, fmt.Errorf("invalid archived source path")
		}
		skip := false
		parts := strings.Split(name, "/")
		for _, part := range parts[:len(parts)-1] {
			if ignored(part) {
				skip = true
				break
			}
		}
		if skip {
			continue
		}
		files++
		size += item.Size
		if files > maxFiles || size > maxBytes || depth(name) > maxDepth {
			return Snapshot{}, ErrBounds
		}
		target := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return Snapshot{}, err
		}
		file, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return Snapshot{}, err
		}
		_, copyErr := io.CopyN(file, reader, item.Size)
		closeErr := file.Close()
		if copyErr != nil {
			return Snapshot{}, copyErr
		}
		if closeErr != nil {
			return Snapshot{}, closeErr
		}
	}
	result, err := Build(ctx, root, project)
	result.SourceRevision = revision
	result.Digest = fmt.Sprintf("%x", sha256.Sum256([]byte(result.Digest+":"+revision)))
	return result, err
}
