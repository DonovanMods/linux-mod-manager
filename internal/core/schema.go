package core

import (
	"context"
	"errors"
	"fmt"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/db"
)

// ErrSchemaTooNew is what every refusal of a database a newer lmm migrated
// matches with errors.Is: the open's (the database was already newer when
// this lmm started) and #648's SchemaUpgradedError (it became newer while
// this lmm ran). It is internal/storage/db's own sentinel, re-exported so a
// frontend can branch on it without importing the storage layer.
var ErrSchemaTooNew = db.ErrSchemaTooNew

// SchemaTooNewCode is SchemaUpgradedError's code in the --json error
// envelope's details (and /api/v1's): the stable name a script or the web
// UI tells this refusal apart by.
const SchemaTooNewCode = "schema_too_new"

// SchemaUpgradedError refuses a mutation, or a long-running frontend's
// read, because another, newer lmm migrated the database after this one
// opened it (#648). Open refuses a database that is already too new; this
// is the same refusal for a process that outlives its open - `lmm serve`,
// or a CLI command that waited on the mutation lock while the newer lmm
// ran. Everything this binary would write or read is shaped for a schema
// that is no longer there, and restarting onto the newer binary is the only
// fix: restarting this one would only refuse again.
//
// Its fields are its --json details: Code is always SchemaTooNewCode,
// Version the schema the database now records, Supported the newest this
// lmm knows.
type SchemaUpgradedError struct {
	Code      string `json:"code"`
	Version   int    `json:"version"`
	Supported int    `json:"supported"`
	// Err is the storage layer's refusal (*db.SchemaTooNewError), kept for
	// errors.Is(err, ErrSchemaTooNew) and errors.As.
	Err error `json:"-"`
}

// newSchemaUpgradedError is the running process's refusal for tooNew.
func newSchemaUpgradedError(tooNew *db.SchemaTooNewError) *SchemaUpgradedError {
	return &SchemaUpgradedError{
		Code:      SchemaTooNewCode,
		Version:   tooNew.Version,
		Supported: tooNew.Supported,
		Err:       tooNew,
	}
}

// Error names what happened and the remedy: this process must be replaced
// by the newer binary, and for the web UI that means its service.
func (e *SchemaUpgradedError) Error() string {
	return fmt.Sprintf("another, newer lmm upgraded the database (schema version %d; this lmm reads up to %d); "+
		"restart this lmm (for the web UI: `lmm service install` or restart lmm serve on the newer binary)",
		e.Version, e.Supported)
}

// Unwrap keeps errors.Is(err, ErrSchemaTooNew) - the open's identity - true.
func (e *SchemaUpgradedError) Unwrap() error { return e.Err }

// Details implements the --json error envelope's extension point.
func (e *SchemaUpgradedError) Details() any { return e }

// SchemaTooNewError is the open's refusal of a database a newer lmm had
// already migrated when this lmm started (NewService): the sentence the
// storage layer has always given, behind "opening database: ", with the
// same details SchemaUpgradedError carries (#648 review), so a script
// branches on one code for both. The remedy differs - this binary is the
// old one, so it says to upgrade lmm - which is why the two stay separate
// types: errors.As tells an open's refusal from a running process's.
type SchemaTooNewError struct {
	Code      string `json:"code"`
	Version   int    `json:"version"`
	Supported int    `json:"supported"`
	// Err is the storage layer's refusal (*db.SchemaTooNewError), kept for
	// errors.Is(err, ErrSchemaTooNew), errors.As and the sentence.
	Err error `json:"-"`
}

// Error is the open's sentence, unchanged.
func (e *SchemaTooNewError) Error() string {
	if e.Err == nil {
		return fmt.Sprintf("opening database: %v (schema version %d; this lmm reads up to %d)", ErrSchemaTooNew, e.Version, e.Supported)
	}
	return "opening database: " + e.Err.Error()
}

// Unwrap keeps errors.Is(err, ErrSchemaTooNew) true.
func (e *SchemaTooNewError) Unwrap() error {
	if e.Err == nil {
		return ErrSchemaTooNew
	}
	return e.Err
}

// Details implements the --json error envelope's extension point.
func (e *SchemaTooNewError) Details() any { return e }

// openRefusal is NewService's error for a failed database open: the
// too-new refusal as a SchemaTooNewError, anything else wrapped as ever.
func openRefusal(err error) error {
	var tooNew *db.SchemaTooNewError
	if errors.As(err, &tooNew) {
		return &SchemaTooNewError{Code: SchemaTooNewCode, Version: tooNew.Version, Supported: tooNew.Supported, Err: err}
	}
	return fmt.Errorf("opening database: %w", err)
}

// CheckSchema reports whether the database is still one this lmm can read:
// nil, or a *SchemaUpgradedError once another, newer lmm has migrated it
// since this Service opened it (#648). Every mutation asks this as it takes
// the slot (beginOp); a long-running frontend asks it before answering a
// read, since a column a migration renamed or rebuilt can fail a query or,
// worse, be misread - `lmm serve` does, for every /api/v1 request. A
// one-shot CLI query need not: its open checked moments ago.
//
// It costs one read of schema_migrations' primary key. A refusal is
// remembered, so once refused it answers from memory: a schema never goes
// back down.
func (s *Service) CheckSchema(ctx context.Context) error {
	if refused := s.schemaRefused.Load(); refused != nil {
		return refused
	}
	if s.db == nil {
		return nil // a struct-literal Service in a test: no database to check
	}
	err := s.db.CheckSchema(ctx)
	if err == nil {
		return nil
	}
	var tooNew *db.SchemaTooNewError
	if !errors.As(err, &tooNew) {
		return fmt.Errorf("checking the database schema: %w", err)
	}
	refused := newSchemaUpgradedError(tooNew)
	s.schemaRefused.CompareAndSwap(nil, refused)
	return s.schemaRefused.Load()
}
