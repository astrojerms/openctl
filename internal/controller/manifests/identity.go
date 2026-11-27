package manifests

import "context"

// ControllerIdentity returns the durable identity shared by migration evidence
// and remote ownership markers. It survives restarts and is unique per database.
func (s *Store) ControllerIdentity(ctx context.Context) (string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()
	id, err := migrationControllerID(ctx, tx)
	if err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return id, nil
}
