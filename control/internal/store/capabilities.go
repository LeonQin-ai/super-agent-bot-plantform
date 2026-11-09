package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/LeonQin-ai/super-agent-bot-plantform/control/internal/execution"
)

// SaveCapabilitySnapshot retains the most recent 1000 observations. SQLite's
// AUTOINCREMENT keeps versions monotonic even when older history is pruned.
func (s *SQLite) SaveCapabilitySnapshot(ctx context.Context, snapshot execution.Snapshot) (uint64, error) {
	snapshot.RegistryVersion = 0
	payload, err := json.Marshal(snapshot)
	if err != nil {
		return 0, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, "INSERT INTO capability_observations(recorded_at,snapshot_json) VALUES(?,?)", timestamp(), string(payload))
	if err != nil {
		return 0, err
	}
	version, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM capability_observations WHERE version <= ?", version-1000); err != nil {
		return 0, err
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	return uint64(version), nil
}

// LatestCapabilitySnapshot is audit data, not a readiness cache.
func (s *SQLite) LatestCapabilitySnapshot(ctx context.Context) (execution.Snapshot, error) {
	var payload string
	var version uint64
	err := s.db.QueryRowContext(ctx, "SELECT version,snapshot_json FROM capability_observations ORDER BY version DESC LIMIT 1").Scan(&version, &payload)
	if errors.Is(err, sql.ErrNoRows) {
		return execution.Snapshot{}, ErrNotFound
	}
	if err != nil {
		return execution.Snapshot{}, err
	}
	var snapshot execution.Snapshot
	if err = json.Unmarshal([]byte(payload), &snapshot); err != nil {
		return execution.Snapshot{}, err
	}
	snapshot.RegistryVersion = version
	return snapshot, nil
}
