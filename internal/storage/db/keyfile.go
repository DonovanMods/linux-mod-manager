package db

// keyfile.go is the key-management half of #79: where the token-encryption
// key lives, when it comes into existence, and what happens when it cannot
// be used.
//
// The key is 32 random bytes in a single file - `<DataDir>/key` in a real
// installation, which app resolves and hands down through core - created
// with O_EXCL on FIRST NEED, so a user who never runs `lmm auth login` never
// has one. The data directory is already 0700 (#80); the file itself is
// 0600 and lmm refuses to use one that is not, rather than quietly
// encrypting under a secret the whole machine can read.

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// TokenKeyFileName is the token-encryption key's file name inside the data
// directory. Exported so the composition root (internal/app) can build the
// path without either layer having to re-spell the name.
const TokenKeyFileName = "key"

// keyFileMode is the only permission set lmm will use a key file under.
const keyFileMode = 0600

// KeyErrorReason classifies why a stored credential could not be read or
// written. Each value has its own remedy, which is the reason they are
// distinguished at all.
type KeyErrorReason string

const (
	// KeyMissing means the key file is not there but encrypted rows are:
	// the credentials cannot be recovered and must be entered again.
	KeyMissing KeyErrorReason = "missing"
	// KeyBadPermissions means the key file is readable by group or other.
	KeyBadPermissions KeyErrorReason = "permissions"
	// KeyMalformed means the file exists but is not a 32-byte key.
	KeyMalformed KeyErrorReason = "malformed"
	// KeyUnreadable means the file could not be read or created at all
	// (a directory in the way, an I/O error, a read-only filesystem).
	KeyUnreadable KeyErrorReason = "unreadable"
	// KeyUndecryptable means the KEY was fine but one row's envelope did
	// not authenticate under it - a corrupt row, or one written under a key
	// that has since been replaced. SourceID names which row.
	KeyUndecryptable KeyErrorReason = "undecryptable"
)

// KeyError reports that lmm could not use the token-encryption key.
//
// Typed because every frontend branches on it: the CLI renders Reason's
// remedy and puts the whole thing in the --json error envelope's "details"
// (via core.TokenKeyError, which carries it to the wire), and `lmm serve`
// answers the Setup page with it. SourceID is set only for
// KeyUndecryptable - the one reason that is about a single row rather than
// the key itself.
type KeyError struct {
	Path     string
	Reason   KeyErrorReason
	SourceID string
	Err      error
}

// Error describes the failure and names the file and the fix.
func (e *KeyError) Error() string {
	switch e.Reason {
	case KeyMissing:
		return fmt.Sprintf("the token-encryption key %s is missing, so stored credentials cannot be read; run `lmm auth login <source>` again to store them under a new key", e.Path)
	case KeyBadPermissions:
		return fmt.Sprintf("the token-encryption key %s is readable by other users; run `chmod 600 %s`", e.Path, e.Path)
	case KeyMalformed:
		return fmt.Sprintf("the token-encryption key %s is not a %d-byte key; move it aside and run `lmm auth login <source>` again to store your credentials under a new key", e.Path, tokenKeySize)
	case KeyUndecryptable:
		return fmt.Sprintf("the stored credential for %q could not be decrypted with %s; run `lmm auth login %s` again", e.SourceID, e.Path, e.SourceID)
	default:
		return fmt.Sprintf("the token-encryption key %s could not be used: %v", e.Path, e.Err)
	}
}

// Unwrap exposes the underlying filesystem or cipher error.
func (e *KeyError) Unwrap() error { return e.Err }

// defaultKeyPath returns the key path to use when none was configured: next
// to the database file. A caller that only knows the database path (db.New,
// which every test uses) still gets encryption rather than a silent
// plaintext fallback; a real installation passes the path explicitly from
// app so no layer below it has to know the XDG layout. An in-memory
// database has no directory and gets an ephemeral key instead ("").
func defaultKeyPath(dbPath string) string {
	if dbPath == ":memory:" {
		return ""
	}
	return filepath.Join(filepath.Dir(dbPath), TokenKeyFileName)
}

// readTokenKey loads the key at path, refusing one that is not exactly
// tokenKeySize bytes or is readable beyond its owner. A missing file is
// reported as KeyMissing so the caller can decide whether to create one.
func readTokenKey(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, &KeyError{Path: path, Reason: KeyMissing, Err: err}
		}
		return nil, &KeyError{Path: path, Reason: KeyUnreadable, Err: err}
	}
	if info.Mode().Perm()&0077 != 0 {
		return nil, &KeyError{Path: path, Reason: KeyBadPermissions}
	}
	key, err := os.ReadFile(path)
	if err != nil {
		return nil, &KeyError{Path: path, Reason: KeyUnreadable, Err: err}
	}
	if len(key) != tokenKeySize {
		return nil, &KeyError{Path: path, Reason: KeyMalformed}
	}
	return key, nil
}

// createTokenKey writes a fresh key at path with O_EXCL, so a key that
// appeared between the read and this call is never overwritten - losing a
// key silently loses every credential encrypted under it. A lost race falls
// back to reading what the winner wrote.
func createTokenKey(path string) ([]byte, error) {
	key, err := newTokenKey()
	if err != nil {
		return nil, &KeyError{Path: path, Reason: KeyUnreadable, Err: err}
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, keyFileMode)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return readTokenKey(path)
		}
		return nil, &KeyError{Path: path, Reason: KeyUnreadable, Err: err}
	}
	if _, err := f.Write(key); err != nil {
		_ = f.Close()
		return nil, &KeyError{Path: path, Reason: KeyUnreadable, Err: err}
	}
	if err := f.Close(); err != nil {
		return nil, &KeyError{Path: path, Reason: KeyUnreadable, Err: err}
	}
	return key, nil
}

// tokenKey returns the key this database encrypts credentials under,
// loading or creating it at most once per *DB.
//
// create distinguishes the two callers: a WRITE (SaveToken, or the legacy
// migration) may bring the key into existence, a READ may not - a missing
// key on a read is the "you deleted it" failure users need told about, not
// an invitation to generate a new one that decrypts nothing.
func (d *DB) tokenKey(create bool) ([]byte, error) {
	d.keyMu.Lock()
	defer d.keyMu.Unlock()

	if d.key != nil {
		return d.key, nil
	}
	if d.keyPath == "" {
		// In-memory database: nothing outlives the process, so the key
		// need not either. Still real encryption, so the same code paths
		// run in tests as in production.
		key, err := newTokenKey()
		if err != nil {
			return nil, err
		}
		d.key = key
		return d.key, nil
	}

	key, err := readTokenKey(d.keyPath)
	var keyErr *KeyError
	if errors.As(err, &keyErr) && keyErr.Reason == KeyMissing && create {
		key, err = createTokenKey(d.keyPath)
	}
	if err != nil {
		return nil, err
	}
	d.key = key
	return d.key, nil
}
