package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// StoredToken is a decrypted API token - the ONE path that hands a
// credential back in the clear, for the sources that have to send it. Every
// status surface uses TokenInfo instead (#79).
type StoredToken struct {
	SourceID  string
	APIKey    string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// TokenInfo is what a credential looks like to anything that only needs to
// know it EXISTS: `lmm auth status`, GET /api/v1/auth, the web UI's Setup
// page. It carries presence, timestamps, and a short fingerprint of the key
// - never the key (#79).
//
// Readable is false for a row whose envelope did not open under the current
// key (corrupt, or written under a key that has since been replaced); its
// Fingerprint is then empty. One such row does not take down the listing -
// a user with three sources and one damaged row needs to be told WHICH one.
type TokenInfo struct {
	SourceID    string
	Fingerprint string
	Readable    bool
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// SaveToken saves or updates an API token for a source, encrypted at rest.
// created_at is set on first insert and preserved across re-logins, so a
// status surface can say how long a credential has been held.
func (d *DB) SaveToken(ctx context.Context, sourceID, apiKey string) error {
	key, err := d.tokenKey(true)
	if err != nil {
		return err
	}
	envelope, err := sealToken(key, sourceID, apiKey)
	if err != nil {
		return err
	}
	_, err = d.ExecContext(ctx, `
        INSERT INTO auth_tokens (source_id, token_data, created_at, updated_at)
        VALUES (?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
        ON CONFLICT(source_id) DO UPDATE SET
            token_data = excluded.token_data,
            updated_at = CURRENT_TIMESTAMP
    `, sourceID, envelope)
	if err != nil {
		return fmt.Errorf("saving token: %w", err)
	}
	return nil
}

// GetToken retrieves and decrypts an API token for a source. This is the
// only method that returns a credential in the clear; it exists for the
// sources that must put the key on a request.
func (d *DB) GetToken(ctx context.Context, sourceID string) (*StoredToken, error) {
	var (
		token     StoredToken
		blob      []byte
		createdAt sql.NullTime
	)
	err := d.QueryRowContext(ctx, `
        SELECT source_id, token_data, created_at, updated_at
        FROM auth_tokens
        WHERE source_id = ?
    `, sourceID).Scan(&token.SourceID, &blob, &createdAt, &token.UpdatedAt)

	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting token: %w", err)
	}
	token.CreatedAt = createdAt.Time

	apiKey, err := d.decryptToken(sourceID, blob)
	if err != nil {
		return nil, err
	}
	token.APIKey = apiKey
	return &token, nil
}

// decryptToken opens one row's envelope. A row with no envelope at all is
// plaintext from a pre-#79 lmm and is returned as-is: migrateTokenEncryption
// normally converts those at open, and refusing to read one it somehow
// missed would lock a user out of their own credential for no gain.
func (d *DB) decryptToken(sourceID string, blob []byte) (string, error) {
	if !isTokenEnvelope(blob) {
		return string(blob), nil
	}
	key, err := d.tokenKey(false)
	if err != nil {
		return "", err
	}
	apiKey, err := openToken(key, sourceID, blob)
	if err != nil {
		return "", &KeyError{Path: d.keyPath, Reason: KeyUndecryptable, SourceID: sourceID, Err: err}
	}
	return apiKey, nil
}

// DeleteToken removes an API token for a source
func (d *DB) DeleteToken(ctx context.Context, sourceID string) error {
	_, err := d.ExecContext(ctx, "DELETE FROM auth_tokens WHERE source_id = ?", sourceID)
	if err != nil {
		return fmt.Errorf("deleting token: %w", err)
	}
	return nil
}

// HasToken checks if a token exists for a source. It reads no ciphertext and
// so never needs the key: a user whose key file is gone can still be told
// that a credential row is there.
func (d *DB) HasToken(ctx context.Context, sourceID string) (bool, error) {
	var count int
	err := d.QueryRowContext(ctx, "SELECT COUNT(*) FROM auth_tokens WHERE source_id = ?", sourceID).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("checking token: %w", err)
	}
	return count > 0, nil
}

// ListTokens returns every stored API token as a TokenInfo - presence,
// timestamps and fingerprint, never the key itself - ordered by source ID,
// regardless of whether that source is still registered. `lmm auth status`
// uses this to surface orphaned tokens (source removed or renamed) that
// would otherwise be invisible.
//
// A single row that will not decrypt is reported as Readable:false rather
// than failing the call; a key file that is missing or unusable ALTOGETHER
// is returned as the error, because then the answer is about the
// installation and not about any one source.
func (d *DB) ListTokens(ctx context.Context) ([]TokenInfo, error) {
	rows, err := d.QueryContext(ctx, `
        SELECT source_id, token_data, created_at, updated_at
        FROM auth_tokens
        ORDER BY source_id
    `)
	if err != nil {
		return nil, fmt.Errorf("listing tokens: %w", err)
	}
	defer rows.Close() //nolint:errcheck

	var tokens []TokenInfo
	for rows.Next() {
		var (
			info      TokenInfo
			blob      []byte
			createdAt sql.NullTime
		)
		if err := rows.Scan(&info.SourceID, &blob, &createdAt, &info.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scanning token: %w", err)
		}
		info.CreatedAt = createdAt.Time

		switch apiKey, err := d.decryptToken(info.SourceID, blob); {
		case err == nil:
			info.Readable = true
			info.Fingerprint = TokenFingerprint(apiKey)
		case isRowLevelKeyError(err):
			// Readable stays false: this row is damaged, the others are fine.
		default:
			return nil, err
		}
		tokens = append(tokens, info)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing tokens: %w", err)
	}
	return tokens, nil
}

// isRowLevelKeyError reports whether err is about one row rather than about
// the key file itself - the distinction that decides whether ListTokens
// degrades a single entry or fails outright.
func isRowLevelKeyError(err error) bool {
	var keyErr *KeyError
	return errors.As(err, &keyErr) && keyErr.Reason == KeyUndecryptable
}
