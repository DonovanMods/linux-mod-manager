package db

import (
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
func (d *DB) SaveToken(sourceID, apiKey string) error {
	_, err := d.Exec(`
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
func (d *DB) GetToken(sourceID string) (*StoredToken, error) {
	var token StoredToken
	err := d.QueryRow(`
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
func (d *DB) DeleteToken(sourceID string) error {
	_, err := d.Exec("DELETE FROM auth_tokens WHERE source_id = ?", sourceID)
	if err != nil {
		return fmt.Errorf("deleting token: %w", err)
	}
	return nil
}

// HasToken checks if a token exists for a source
func (d *DB) HasToken(sourceID string) (bool, error) {
	var count int
	err := d.QueryRow("SELECT COUNT(*) FROM auth_tokens WHERE source_id = ?", sourceID).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("checking token: %w", err)
	}
	return count > 0, nil
}
