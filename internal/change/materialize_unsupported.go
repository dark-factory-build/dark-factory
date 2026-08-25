//go:build !darwin

package change

import (
	"context"
	"runtime"
)

// Prepared exists only to keep unsupported builds source-compatible; Prepare
// never returns one on those platforms.
type Prepared struct{}

func Prepare(context.Context, string, string, string) (*Prepared, error) {
	return nil, &UnsupportedError{Platform: runtime.GOOS}
}

func (p *Prepared) Identity() StageIdentity { return StageIdentity{} }

func (p *Prepared) PopulateAndPublish(context.Context, Manifest, BlobSource) (Published, error) {
	return Published{}, &UnsupportedError{Platform: runtime.GOOS}
}

func (p *Prepared) Close() error { return &UnsupportedError{Platform: runtime.GOOS} }

func InspectPublished(context.Context, string, string, StageIdentity, ObjectFormat, ObjectID) (TreeFacts, error) {
	return TreeFacts{}, &UnsupportedError{Platform: runtime.GOOS}
}

func RemoveRecordedTree(context.Context, string, string, StageIdentity) error {
	return &UnsupportedError{Platform: runtime.GOOS}
}
