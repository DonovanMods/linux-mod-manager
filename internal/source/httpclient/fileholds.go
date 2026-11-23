// Package httpclient: this file is the PERSISTED hold store (#675, lifted
// from thunderstore/hold.go, #436 T3 review F3 and F9) - when lmm has
// decided not to ask a host, or one part of it, again before a given time,
// and why, kept in a file every lmm process reads.
//
// "Not asking NexusMods again until 14:03" is a promise about the host, not
// about one process. A script running `lmm update` and then `lmm verify`,
// or a user who simply runs the command again, starts a new process each
// time, and each one used to ask at once - and so prolonged the very block
// it was told about. Every request now reads the file first; a change is
// made under an advisory lock and published with a rename, so two lmm
// processes recording failures at the same moment keep both, and a reader
// never sees half a file.
//
// A hold has one of two scopes: the HOST's (scope ""), which holds every
// request, or one named SCOPE - a Thunderstore community, a NexusMods API
// key's fingerprint - which holds only the requests about it. Either is a
// hold for one of two reasons, as MemoryHolds': the host NAMED a wait,
// which stands until it expires, or lmm tripped its own breaker after
// Threshold failures in a row, which the next success lifts.
//
// The file is lmm's own and only ever advisory: one that is missing,
// unreadable or malformed holds nothing, a scope the store's ValidScope
// refuses is dropped, and a hold that ends further ahead than lmm ever sets
// one is ignored rather than obeyed - a damaged file must not lock lmm out
// of a host. A hold that has expired is ignored and, the next time a
// process looks, pruned; a file left holding nothing is removed.
package httpclient

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	// holdsSchema is the file's format version; any other reads as empty.
	holdsSchema = 1
	// maxHoldsFileBytes bounds what a read will take in.
	maxHoldsFileBytes = 1 << 20
	// defaultHoldsLockWait bounds the wait for another process's change. A
	// change is a few hundred bytes; anything longer is a process that is
	// stuck, and a hold this process cannot write still holds it, in memory.
	defaultHoldsLockWait = 2 * time.Second
	// holdsLockPoll is how often that wait retries: flock has no timed form.
	holdsLockPoll = 25 * time.Millisecond
	// holdsSanitySlack is how far past MaxHold (or the present, for a
	// failure's date) a record may lie before it reads as something lmm
	// never wrote.
	holdsSanitySlack = time.Minute
	// defaultScopesField is the JSON member scoped holds are kept under.
	defaultScopesField = "scopes"
)

// HoldRecord is one scope's state, as the file keeps it.
type HoldRecord struct {
	// Until is when lmm will next ask; zero or past means it may now.
	Until time.Time `json:"until,omitzero"`
	// Reason is why not before Until.
	Reason string `json:"reason,omitempty"`
	// Named reports that the host itself named the wait, which a success
	// elsewhere does not lift.
	Named bool `json:"named,omitempty"`
	// Failures counts failed requests in a row; LastFailure dates the
	// streak, which ends once the cooldown has passed without another.
	Failures    int       `json:"failures,omitempty"`
	LastFailure time.Time `json:"last_failure,omitzero"`
}

// ActiveAt reports whether the hold is in force at now.
func (r HoldRecord) ActiveAt(now time.Time) bool { return !r.Until.IsZero() && now.Before(r.Until) }

// isZero reports a record that records nothing at all.
func (r HoldRecord) isZero() bool {
	return r.Until.IsZero() && r.Reason == "" && !r.Named && r.Failures == 0 && r.LastFailure.IsZero()
}

// empty reports whether the record says nothing worth keeping at now.
func (r HoldRecord) empty(now time.Time, cooldown time.Duration) bool {
	return !r.ActiveAt(now) && (r.Failures == 0 || now.Sub(r.LastFailure) > cooldown)
}

// ScopedHold is one hold in force and the scope it belongs to ("" for the
// host's).
type ScopedHold struct {
	Scope string
	HoldRecord
}

// FileHoldsConfig is a FileHolds' file and rules.
type FileHoldsConfig struct {
	// Path is the hold file. Its lock is the same name with a ".lock"
	// extension, beside it. Empty keeps the holds in memory, for the life
	// of the process, which is all a store with nowhere to write can
	// promise.
	Path string
	// ScopesField is the JSON member the scoped holds are kept under
	// ("communities" for Thunderstore's file, which predates this store).
	// Empty is "scopes".
	ScopesField string
	// ValidScope reports whether a scope name read from the file is one
	// this store could have written; any other is dropped. Nil accepts
	// none - a store of host holds only.
	ValidScope func(string) bool
	// Now is the clock holds are judged against; nil is time.Now.
	Now func() time.Time
	// Threshold failed requests in a row trip a breaker that holds for
	// Cooldown, and a streak lasts Cooldown without another failure. Zero
	// never trips one.
	Threshold int
	Cooldown  time.Duration
	// MaxHold caps any one named wait; a record ending further ahead than
	// this (and a minute's slack) is not obeyed. Zero is no cap on a write,
	// and a day on a read.
	MaxHold time.Duration
	// DirMode is the mode the file's directory is created with, if it
	// does not exist. Zero is 0o755.
	DirMode os.FileMode
	// LockWait bounds the wait for another process's change. Zero is two
	// seconds.
	LockWait time.Duration
}

// FileHolds is the persisted hold store. It is safe for concurrent use, in
// one process and across many.
type FileHolds struct {
	cfg      FileHoldsConfig
	lockPath string

	mu  sync.Mutex
	mem holdState
	// diskFailed reports that the last change could not be written, so mem
	// holds something the file does not; reads overlay it until a write
	// succeeds.
	diskFailed bool
}

// holdState is the file's content, decoded.
type holdState struct {
	Host   HoldRecord
	Scopes map[string]HoldRecord
}

// NewFileHolds returns a store over cfg.Path.
func NewFileHolds(cfg FileHoldsConfig) *FileHolds {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.ScopesField == "" {
		cfg.ScopesField = defaultScopesField
	}
	if cfg.DirMode == 0 {
		cfg.DirMode = 0o755
	}
	if cfg.LockWait == 0 {
		cfg.LockWait = defaultHoldsLockWait
	}
	h := &FileHolds{cfg: cfg}
	if cfg.Path != "" {
		h.lockPath = strings.TrimSuffix(cfg.Path, filepath.Ext(cfg.Path)) + ".lock"
	}
	return h
}

// Path is the hold file, or "" for a store kept in memory.
func (h *FileHolds) Path() string { return h.cfg.Path }

// Active reports the hold in force for a request about scope - the host's,
// or scope's own, whichever ends later - and the scope it belongs to (""
// for the host). Scope "" asks about the host alone.
func (h *FileHolds) Active(scope string) (HoldRecord, string, bool) {
	h.mu.Lock()
	now := h.cfg.Now()
	state, stale := h.readLocked(now)
	h.mu.Unlock()
	if stale {
		h.cleanUp()
	}
	best, owner, found := HoldRecord{}, "", false
	if state.Host.ActiveAt(now) {
		best, found = state.Host, true
	}
	if scope != "" {
		if own := state.Scopes[scope]; own.ActiveAt(now) && (!found || own.Until.After(best.Until)) {
			best, owner, found = own, scope, true
		}
	}
	return best, owner, found
}

// List reports every hold in force, the host's first, then the scopes' by
// name.
func (h *FileHolds) List() []ScopedHold {
	h.mu.Lock()
	now := h.cfg.Now()
	state, stale := h.readLocked(now)
	h.mu.Unlock()
	if stale {
		h.cleanUp()
	}
	var out []ScopedHold
	if state.Host.ActiveAt(now) {
		out = append(out, ScopedHold{HoldRecord: state.Host})
	}
	names := make([]string, 0, len(state.Scopes))
	for name, rec := range state.Scopes {
		if rec.ActiveAt(now) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		out = append(out, ScopedHold{Scope: name, HoldRecord: state.Scopes[name]})
	}
	return out
}

// Snapshot is every record the store holds, in force or not: the host's
// and each scope's.
func (h *FileHolds) Snapshot() (HoldRecord, map[string]HoldRecord) {
	h.mu.Lock()
	defer h.mu.Unlock()
	state, _ := h.readLocked(h.cfg.Now())
	return state.Host, state.Scopes
}

// Failed records one failed request for scope ("" for the host), and trips
// the hold once Threshold have failed in a row. It returns the scope's
// record afterwards.
func (h *FileHolds) Failed(scope, why string) HoldRecord {
	var out HoldRecord
	h.update(func(st *holdState, now time.Time) {
		rec := st.get(scope)
		if !rec.Until.IsZero() && !rec.ActiveAt(now) {
			// An expired hold is over, streak and all: the request that
			// failed was the probe it allowed, not the next strike of the
			// outage that set it.
			rec = HoldRecord{}
		}
		if rec.Failures > 0 && now.Sub(rec.LastFailure) > h.cfg.Cooldown {
			rec.Failures = 0
		}
		rec.Failures++
		rec.LastFailure = now
		if h.cfg.Threshold > 0 && rec.Failures >= h.cfg.Threshold && !rec.ActiveAt(now) {
			rec.Until = ceilSecond(now.Add(h.cfg.Cooldown))
			rec.Reason = fmt.Sprintf("suspended after %d failed requests in a row; the last: %s", rec.Failures, why)
			rec.Named = false
		}
		st.set(scope, rec)
		out = rec
	})
	return out
}

// Named records a wait the host asked for, for scope ("" for the host),
// never SHORTENING a hold already in force: a server that asked for ten
// minutes is not re-asked after a later, shorter one. It returns the
// scope's record afterwards.
func (h *FileHolds) Named(scope string, until time.Time, why string) HoldRecord {
	var out HoldRecord
	h.update(func(st *holdState, now time.Time) {
		rec := st.get(scope)
		if h.cfg.MaxHold > 0 && until.After(now.Add(h.cfg.MaxHold)) {
			until = now.Add(h.cfg.MaxHold)
		}
		until = ceilSecond(until)
		if !rec.ActiveAt(now) || until.After(rec.Until) {
			rec.Until, rec.Reason, rec.Named = until, why, true
		}
		st.set(scope, rec)
		out = rec
	})
	return out
}

// Succeeded clears scope's failure streak, and a hold lmm set itself: one
// good answer means the host - or the scope - is back. A wait the host
// NAMED stands until it expires: an answer to a request that was already
// in flight says nothing about the time it asked for.
func (h *FileHolds) Succeeded(scope string) {
	h.mu.Lock()
	state, _ := h.readLocked(h.cfg.Now())
	rec := state.get(scope)
	h.mu.Unlock()
	if rec.Failures == 0 && rec.Until.IsZero() {
		return // nothing to clear: no write on the common path
	}
	h.update(func(st *holdState, now time.Time) {
		rec := st.get(scope)
		rec.Failures, rec.LastFailure = 0, time.Time{}
		if !rec.Named || !rec.ActiveAt(now) {
			rec.Until, rec.Reason, rec.Named = time.Time{}, "", false
		}
		st.set(scope, rec)
	})
}

// cleanUp rewrites the file without the records that no longer say
// anything - expired holds, streaks past their cooldown - and removes a
// file left holding nothing.
func (h *FileHolds) cleanUp() { h.update(func(*holdState, time.Time) {}) }

// update applies fn to the current state and publishes the result. It is
// the one writer: under this process's mutex and the cross-process flock,
// read, change, write-then-rename.
func (h *FileHolds) update(fn func(*holdState, time.Time)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.cfg.Now()
	if h.cfg.Path == "" {
		fn(&h.mem, now)
		h.mem.prune(now, h.cfg.Cooldown)
		return
	}
	unlock, lockErr := h.lockFile()
	state, _ := h.readLocked(now)
	fn(&state, now)
	state.prune(now, h.cfg.Cooldown)
	h.mem = state
	if lockErr != nil {
		h.diskFailed = true
		return
	}
	defer unlock()
	if err := h.write(state); err != nil {
		h.diskFailed = true
		return
	}
	h.diskFailed = false
}

// readLocked is the state in force: the file's, or - when the last change
// could not be written - the file's with this process's own holds laid over
// it. stale reports a record the next write would prune. h.mu must be held.
func (h *FileHolds) readLocked(now time.Time) (holdState, bool) {
	if h.cfg.Path == "" {
		return h.mem.clone(), false
	}
	state := h.read(now)
	stale := state.stale(now, h.cfg.Cooldown)
	if h.diskFailed {
		state = state.overlay(h.mem, now)
		stale = false // the file cannot be written; do not keep trying on every read
	}
	return state, stale
}

// lockFile takes the flock that serialises changes, waiting at most
// LockWait. The lock file is never opened through a symbolic link.
func (h *FileHolds) lockFile() (func(), error) {
	if err := os.MkdirAll(filepath.Dir(h.lockPath), h.cfg.DirMode); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(h.lockPath, os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0o600)
	if err != nil {
		return nil, err
	}
	if info, err := file.Stat(); err != nil || !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, fmt.Errorf("%s is not a plain file", h.lockPath)
	}
	deadline := time.Now().Add(h.cfg.LockWait)
	for {
		err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() {
				_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
				_ = file.Close()
			}, nil
		}
		if err != syscall.EWOULDBLOCK || time.Now().After(deadline) { //nolint:errorlint // Flock returns a bare syscall.Errno
			_ = file.Close()
			return nil, fmt.Errorf("locking %s: %w", h.lockPath, err)
		}
		time.Sleep(holdsLockPoll)
	}
}

// read reads and sanitises the file. Anything wrong with it reads as no
// holds at all.
func (h *FileHolds) read(now time.Time) holdState {
	file, err := os.Open(h.cfg.Path)
	if err != nil {
		return holdState{}
	}
	defer func() { _ = file.Close() }()
	if info, err := file.Stat(); err != nil || !info.Mode().IsRegular() || info.Size() > maxHoldsFileBytes {
		return holdState{}
	}
	data, err := io.ReadAll(io.LimitReader(file, maxHoldsFileBytes+1))
	if err != nil || len(data) > maxHoldsFileBytes {
		return holdState{}
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(data, &doc); err != nil {
		return holdState{}
	}
	var schema int
	if err := json.Unmarshal(doc["schema"], &schema); err != nil || schema != holdsSchema {
		return holdState{}
	}
	var host HoldRecord
	if raw, ok := doc["host"]; ok && json.Unmarshal(raw, &host) != nil {
		return holdState{}
	}
	var scopes map[string]HoldRecord
	if raw, ok := doc[h.cfg.ScopesField]; ok && json.Unmarshal(raw, &scopes) != nil {
		return holdState{}
	}
	out := holdState{Host: h.sanitize(host, now)}
	for name, rec := range scopes {
		if name != "" && h.cfg.ValidScope != nil && h.cfg.ValidScope(name) {
			out.set(name, h.sanitize(rec, now))
		}
	}
	return out
}

// sanitize drops what lmm could never have written: a hold ending further
// ahead than MaxHold, a failure dated in the future, a negative streak.
// Such a record is not obeyed - it is noise.
func (h *FileHolds) sanitize(r HoldRecord, now time.Time) HoldRecord {
	limit := h.cfg.MaxHold
	if limit <= 0 {
		limit = 24 * time.Hour
	}
	if r.Until.After(now.Add(limit+holdsSanitySlack)) || r.LastFailure.After(now.Add(holdsSanitySlack)) || r.Failures < 0 {
		return HoldRecord{}
	}
	return r
}

// write publishes state write-then-rename, or removes the file when state
// holds nothing. The lock must be held.
func (h *FileHolds) write(state holdState) error {
	if state.isZero() {
		if err := os.Remove(h.cfg.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	doc := map[string]any{"schema": holdsSchema, "host": state.Host}
	if len(state.Scopes) > 0 {
		doc[h.cfg.ScopesField] = state.Scopes
	}
	data, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	return writeFileAtomic(h.cfg.Path, data, h.cfg.DirMode)
}

// writeFileAtomic writes data to path write-then-rename, with mode 0600:
// the file is staged beside path, synced, and renamed over it, so a reader
// sees the old file or the new one, never half of either.
func writeFileAtomic(path string, data []byte, dirMode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".stage-*")
	if err != nil {
		return fmt.Errorf("staging in %s: %w", dir, err)
	}
	name := tmp.Name()
	fail := func(err error) error {
		_ = tmp.Close()
		_ = os.Remove(name)
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		return fail(fmt.Errorf("writing %s: %w", name, err))
	}
	if err := tmp.Sync(); err != nil {
		return fail(fmt.Errorf("syncing %s: %w", name, err))
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("closing %s: %w", name, err)
	}
	if err := os.Rename(name, path); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("publishing %s: %w", path, err)
	}
	return nil
}

// get returns scope's record; scope "" is the host's.
func (st holdState) get(scope string) HoldRecord {
	if scope == "" {
		return st.Host
	}
	return st.Scopes[scope]
}

// set replaces scope's record; scope "" is the host's.
func (st *holdState) set(scope string, r HoldRecord) {
	if scope == "" {
		st.Host = r
		return
	}
	if st.Scopes == nil {
		st.Scopes = map[string]HoldRecord{}
	}
	st.Scopes[scope] = r
}

// prune drops records that no longer say anything, so the file does not
// grow with every scope that ever failed once.
func (st *holdState) prune(now time.Time, cooldown time.Duration) {
	for name, rec := range st.Scopes {
		if rec.empty(now, cooldown) {
			delete(st.Scopes, name)
		}
	}
	if len(st.Scopes) == 0 {
		st.Scopes = nil
	}
	if st.Host.empty(now, cooldown) {
		st.Host = HoldRecord{}
	}
}

// stale reports a record prune would drop.
func (st holdState) stale(now time.Time, cooldown time.Duration) bool {
	if !st.Host.isZero() && st.Host.empty(now, cooldown) {
		return true
	}
	for _, rec := range st.Scopes {
		if rec.empty(now, cooldown) {
			return true
		}
	}
	return false
}

// isZero reports a state that records nothing at all.
func (st holdState) isZero() bool {
	return st.Host.isZero() && len(st.Scopes) == 0
}

// overlay lays mine's holds over st wherever mine's ends later.
func (st holdState) overlay(mine holdState, now time.Time) holdState {
	out := st.clone()
	if mine.Host.ActiveAt(now) && mine.Host.Until.After(out.Host.Until) {
		out.Host = mine.Host
	}
	for name, rec := range mine.Scopes {
		if rec.ActiveAt(now) && rec.Until.After(out.get(name).Until) {
			out.set(name, rec)
		}
	}
	return out
}

func (st holdState) clone() holdState {
	out := holdState{Host: st.Host}
	for name, rec := range st.Scopes {
		out.set(name, rec)
	}
	return out
}
