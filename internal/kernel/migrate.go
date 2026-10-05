package kernel

import (
	"context"
	"errors"
	"fmt"
)

// migrateLegacy refuses a home at any user_version but the current one. Open
// calls it with the writer before the store is published, so a schema change
// bumps userVersion and adds its one step from the version before it here; the
// preflight then accepts that version's exact schema too. The writer checkout
// also happens before Open refreshes its pinned sidecar facts, which the
// sidecar binding relies on.
func (store *Store) migrateLegacy(ctx context.Context) error {
	connection, err := store.writerConnection(ctx)
	if err != nil {
		return err
	}
	_, version, err := inspectIdentity(ctx, connection)
	if err != nil {
		releaseUncertainConnection(connection)
		return err
	}
	if version != userVersion {
		return errors.Join(fmt.Errorf("%w: home is at user_version %d, this build requires %d", ErrForeignDatabase, version, userVersion), connection.Close())
	}
	return connection.Close()
}
