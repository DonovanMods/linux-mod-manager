package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// StoredToken represents an API token stored in the database
type StoredToken struct {
	SourceID  string
	APIKey    string
	UpdatedAt time.Time
}

// SaveToken saves or updates an API token for a source
func (d *DB) SaveToken(ctx context.Context, sourceID, apiKey string) error {
	_, err := d.ExecContext(ctx, `
        INSERT INTO auth_tokens (source_id, token_data, updated_at)
        VALUES (?, ?, CURRENT_TIMESTAMP)
        ON CONFLICT(source_id) DO UPDATE SET
            token_data = excluded.token_data,
            updated_at = CURRENT_TIMESTAMP
    `, sourceID, apiKey)
	if err != nil {
		return fmt.Errorf("saving token: %w", err)
	}
	return nil
}

// GetToken retrieves an API token for a source
func (d *DB) GetToken(ctx context.Context, sourceID string) (*StoredToken, error) {
	var token StoredToken
	err := d.QueryRowContext(ctx, `
        SELECT source_id, token_data, updated_at
        FROM auth_tokens
        WHERE source_id = ?
    `, sourceID).Scan(&token.SourceID, &token.APIKey, &token.UpdatedAt)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting token: %w", err)
	}
	return &token, nil
}

// DeleteToken removes an API token for a source
func (d *DB) DeleteToken(ctx context.Context, sourceID string) error {
	_, err := d.ExecContext(ctx, "DELETE FROM auth_tokens WHERE source_id = ?", sourceID)
	if err != nil {
		return fmt.Errorf("deleting token: %w", err)
	}
	return nil
}

// HasToken checks if a token exists for a source
func (d *DB) HasToken(ctx context.Context, sourceID string) (bool, error) {
	var count int
	err := d.QueryRowContext(ctx, "SELECT COUNT(*) FROM auth_tokens WHERE source_id = ?", sourceID).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("checking token: %w", err)
	}
	return count > 0, nil
}

// ListTokens returns every stored API token, ordered by source ID,
// regardless of whether that source is still registered. `lmm auth status`
// uses this to surface orphaned tokens (source removed or renamed) that
// would otherwise be invisible.
func (d *DB) ListTokens(ctx context.Context) ([]StoredToken, error) {
	rows, err := d.QueryContext(ctx, `
        SELECT source_id, token_data, updated_at
        FROM auth_tokens
        ORDER BY source_id
    `)
	if err != nil {
		return nil, fmt.Errorf("listing tokens: %w", err)
	}
	defer rows.Close() //nolint:errcheck

	var tokens []StoredToken
	for rows.Next() {
		var token StoredToken
		if err := rows.Scan(&token.SourceID, &token.APIKey, &token.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scanning token: %w", err)
		}
		tokens = append(tokens, token)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing tokens: %w", err)
	}
	return tokens, nil
}
