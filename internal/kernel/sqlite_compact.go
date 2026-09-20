package kernel

import (
	"context"
	"fmt"
)

// CompactStorage is explicit maintenance: retain the writer gate across VACUUM
// (which cannot run inside a transaction), so dispatch cannot restart mid-copy.
func (store *Store) CompactStorage(ctx context.Context) (FactoryState, error) {
	tx, err := store.beginValidatedWrite(ctx)
	if err != nil {
		return FactoryState{}, err
	}
	defer tx.Close()
	state, err := factoryState(ctx, tx.connection)
	if err != nil {
		return FactoryState{}, tx.Rollback(err)
	}
	var live bool
	if err := tx.connection.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runs WHERE phase <> 'terminal')`).Scan(&live); err != nil {
		return FactoryState{}, tx.Rollback(err)
	}
	if state.DispatchEnabled || live {
		return FactoryState{}, tx.Rollback(fmt.Errorf("%w: compaction requires dispatch off and no nonterminal runs", ErrConflict))
	}
	if err := tx.Rollback(nil); err != nil {
		return FactoryState{}, err
	}
	if _, err := tx.connection.ExecContext(ctx, `VACUUM`); err != nil {
		return FactoryState{}, err
	}
	var busy, pages, checkpointed int
	if err := tx.connection.QueryRowContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &pages, &checkpointed); err != nil {
		return FactoryState{}, err
	}
	if busy != 0 {
		return FactoryState{}, ErrBusy
	}
	if err := validateExactSchema(ctx, tx.connection); err != nil {
		return FactoryState{}, err
	}
	if err := validateDurableControls(ctx, tx.connection); err != nil {
		return FactoryState{}, err
	}
	return state, nil
}
