// Package thunderstore: this file is the HOLD store (#436, T3 review F3 and
// F9) - when lmm has decided not to ask Thunderstore, or one community on
// it, again before a given time, and why.
//
// A hold is PERSISTED beside the community indexes, because "not asking
// Thunderstore again until 18:10" is a promise about the host, not about
// one process. A script looping over `lmm search`, or a user who simply
// runs the command again, starts a new process each time, and each one used
// to ask at once. Every request now reads the file first; a change is made
// under an advisory lock and published with a rename, so two lmm processes
// recording failures at the same moment keep both, and a reader never sees
// half a file.
//
// A hold has one of two scopes (review F9, decided):
//
//   - the HOST: a throttle (429) or a server failure (5xx) comes from the
//     host every community shares, so it holds all of them;
//   - one COMMUNITY: a 404, or a document that is not a package list or
//     will not parse, belongs to that community alone, and holding every
//     other one for it would be wrong.
//
// Either scope is a hold for one of two reasons. The host NAMED a wait
// (Retry-After past what lmm sits through), which stands until it expires -
// an answer from some other request in the meantime does not lift it. Or
// lmm tripped its own breaker after breakerThreshold failures in a row,
// which the next success lifts.
//
// The file is lmm's own and only ever advisory: one that is missing,
// unreadable or malformed holds nothing, and a hold that ends further ahead
// than lmm ever sets one is ignored rather than obeyed - a damaged file must
// not lock lmm out of Thunderstore.
package thunderstore

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"syscall"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
)

const (
	// holdsFileName is the persisted hold state, in the index root. The "."
	// prefix keeps it out of the community listing: no slug starts with one.
	holdsFileName = ".holds.json"
	// holdsLockFileName is the flock target that serialises changes to it.
	holdsLockFileName = ".holds.lock"
	// holdsSchema is the file's format version; any other reads as empty.
	holdsSchema = 1
	// maxHoldsFileBytes bounds what a read will take in.
	maxHoldsFileBytes = 1 << 20
	// holdsLockWait bounds the wait for another process's change. A change
	// is a few hundred bytes; anything longer is a process that is stuck,
	// and a hold this process cannot write still holds it, in memory.
	holdsLockWait = 2 * time.Second
	// maxHold is the longest lmm ever holds off (review F8): a Retry-After
	// asking for more is honoured for this long. A day is far past any
	// throttle window a real host names, and short enough that a bogus
	// header cannot shut lmm out of Thunderstore for a year.
	maxHold = 24 * time.Hour
)

// hold is one scope's state.
type hold struct {
	// Until is when lmm will next ask; zero or past means it may now.
	Until time.Time `json:"until,omitzero"`
	// Reason is why not before Until.
	Reason string `json:"reason,omitempty"`
	// Named reports that the host itself named the wait, which a success
	// elsewhere does not lift.
	Named bool `json:"named,omitempty"`
	// Failures counts failed requests in a row; LastFailure dates the
	// streak, which ends once breakerCooldown has passed without another.
	Failures    int       `json:"failures,omitempty"`
	LastFailure time.Time `json:"last_failure,omitzero"`
}

// activeAt reports whether the hold is in force at now.
func (h hold) activeAt(now time.Time) bool { return !h.Until.IsZero() && now.Before(h.Until) }

// empty reports whether the hold records nothing worth keeping at now.
func (h hold) empty(now time.Time) bool {
	return !h.activeAt(now) && (h.Failures == 0 || now.Sub(h.LastFailure) > breakerCooldown)
}

// holdFile is the file's shape.
type holdFile struct {
	Schema      int             `json:"schema"`
	Host        hold            `json:"host"`
	Communities map[string]hold `json:"communities,omitempty"`
}

// holdStore reads and changes the hold state. With no index root it keeps
// the state in memory, for the life of the process, which is all a source
// with nowhere to write can promise.
type holdStore struct {
	path     string
	lockPath string
	now      func() time.Time

	mu  sync.Mutex
	mem holdFile
	// diskFailed reports that the last change could not be written, so mem
	// holds something the file does not; reads overlay it until a write
	// succeeds.
	diskFailed bool
}

func newHoldStore(root string, now func() time.Time) *holdStore {
	h := &holdStore{now: now}
	if root != "" {
		h.path = filepath.Join(root, holdsFileName)
		h.lockPath = filepath.Join(root, holdsLockFileName)
	}
	return h
}

// get returns community's hold; community "" is the host's.
func (st holdFile) get(community string) hold {
	if community == "" {
		return st.Host
	}
	return st.Communities[community]
}

// set replaces community's hold; community "" is the host's.
func (st *holdFile) set(community string, h hold) {
	if community == "" {
		st.Host = h
		return
	}
	if st.Communities == nil {
		st.Communities = map[string]hold{}
	}
	st.Communities[community] = h
}

// active reports the hold in force for a request about community - the
// host's, or community's own, whichever ends later - and the scope it
// belongs to ("" for the host). community "" asks about the host alone.
func (h *holdStore) active(community string) (hold, string, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()
	state := h.readLocked(now)
	best, scope, found := hold{}, "", false
	if host := state.Host; host.activeAt(now) {
		best, found = host, true
	}
	if community != "" {
		if own := state.Communities[community]; own.activeAt(now) && (!found || own.Until.After(best.Until)) {
			best, scope, found = own, community, true
		}
	}
	return best, scope, found
}

// failed records one failed request for community's scope ("" for the
// host), and trips the hold once breakerThreshold have failed in a row. It
// returns the scope's hold afterwards.
func (h *holdStore) failed(community, why string) hold {
	var out hold
	h.update(func(st *holdFile, now time.Time) {
		rec := st.get(community)
		if !rec.Until.IsZero() && !rec.activeAt(now) {
			// An expired hold is over, streak and all: the request that
			// failed was the probe it allowed, not the next strike of the
			// outage that set it.
			rec = hold{}
		}
		if rec.Failures > 0 && now.Sub(rec.LastFailure) > breakerCooldown {
			rec.Failures = 0
		}
		rec.Failures++
		rec.LastFailure = now
		if rec.Failures >= breakerThreshold && !rec.activeAt(now) {
			rec.Until = ceilSecond(now.Add(breakerCooldown))
			rec.Reason = fmt.Sprintf("suspended after %d failed requests in a row; the last: %s", rec.Failures, why)
			rec.Named = false
		}
		st.set(community, rec)
		out = rec
	})
	return out
}

// named records a wait the host asked for, never SHORTENING a hold already
// in force: a server that asked for ten minutes is not re-asked after a
// later, shorter one.
func (h *holdStore) named(community string, until time.Time, why string) hold {
	var out hold
	h.update(func(st *holdFile, now time.Time) {
		rec := st.get(community)
		until = ceilSecond(minTime(until, now.Add(maxHold)))
		if !rec.activeAt(now) || until.After(rec.Until) {
			rec.Until, rec.Reason, rec.Named = until, why, true
		}
		st.set(community, rec)
		out = rec
	})
	return out
}

// succeeded clears community's failure streak, and a hold lmm set itself:
// one good answer means the host - or the community - is back. A wait the
// host NAMED stands until it expires (review F9): an answer to a request
// that was already in flight says nothing about the time it asked for.
func (h *holdStore) succeeded(community string) {
	h.mu.Lock()
	state := h.readLocked(h.now())
	rec := state.get(community)
	h.mu.Unlock()
	if rec.Failures == 0 && rec.Until.IsZero() {
		return // nothing to clear: no write on the common path
	}
	h.update(func(st *holdFile, now time.Time) {
		rec := st.get(community)
		rec.Failures, rec.LastFailure = 0, time.Time{}
		if !rec.Named || !rec.activeAt(now) {
			rec.Until, rec.Reason, rec.Named = time.Time{}, "", false
		}
		st.set(community, rec)
	})
}

// Holds implements source.HoldReporter: every hold in force, host first,
// then communities by name (T3 review F10) - what a frontend shows without
// asking the host anything.
func (s *Source) Holds(context.Context) []source.Hold { return s.holds.list() }

// list reports every hold in force, host first, then communities by name.
func (h *holdStore) list() []source.Hold {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()
	state := h.readLocked(now)
	var out []source.Hold
	if state.Host.activeAt(now) {
		out = append(out, source.Hold{Source: serviceName, Until: state.Host.Until, Reason: state.Host.Reason})
	}
	names := make([]string, 0, len(state.Communities))
	for name, rec := range state.Communities {
		if rec.activeAt(now) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		rec := state.Communities[name]
		out = append(out, source.Hold{Source: serviceName, GameID: name, Until: rec.Until, Reason: rec.Reason})
	}
	return out
}

// update applies fn to the current state and publishes the result. It is
// the one writer: under this process's mutex and the cross-process flock,
// read, change, write-then-rename.
func (h *holdStore) update(fn func(*holdFile, time.Time)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()
	if h.path == "" {
		fn(&h.mem, now)
		return
	}
	unlock, lockErr := h.lockFile()
	state := h.readLocked(now)
	fn(&state, now)
	state.prune(now)
	h.mem = state
	if lockErr != nil {
		h.diskFailed = true
		return
	}
	defer unlock()
	if err := writeHoldFile(h.path, state); err != nil {
		h.diskFailed = true
		return
	}
	h.diskFailed = false
}

// readLocked is the state in force: the file's, or - when the last change
// could not be written - the file's with this process's own holds laid over
// it. h.mu must be held.
func (h *holdStore) readLocked(now time.Time) holdFile {
	if h.path == "" {
		return h.mem.clone()
	}
	state := readHoldFile(h.path, now)
	if h.diskFailed {
		state = state.overlay(h.mem, now)
	}
	return state
}

// lockFile takes the flock that serialises changes, waiting at most
// holdsLockWait. The lock file is never opened through a symbolic link.
func (h *holdStore) lockFile() (func(), error) {
	if err := os.MkdirAll(filepath.Dir(h.lockPath), 0o755); err != nil {
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
	deadline := time.Now().Add(holdsLockWait)
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
		time.Sleep(indexLockPoll)
	}
}

// readHoldFile reads and sanitises the file. Anything wrong with it reads
// as no holds at all.
func readHoldFile(path string, now time.Time) holdFile {
	file, err := os.Open(path)
	if err != nil {
		return holdFile{}
	}
	defer func() { _ = file.Close() }()
	if info, err := file.Stat(); err != nil || !info.Mode().IsRegular() || info.Size() > maxHoldsFileBytes {
		return holdFile{}
	}
	data, err := io.ReadAll(io.LimitReader(file, maxHoldsFileBytes+1))
	if err != nil || len(data) > maxHoldsFileBytes {
		return holdFile{}
	}
	var state holdFile
	if err := json.Unmarshal(data, &state); err != nil || state.Schema != holdsSchema {
		return holdFile{}
	}
	out := holdFile{Host: sanitizeHold(state.Host, now)}
	for name, rec := range state.Communities {
		if communityPattern.MatchString(name) {
			out.set(name, sanitizeHold(rec, now))
		}
	}
	return out
}

// sanitizeHold drops what lmm could never have written: a hold ending
// further ahead than maxHold, a failure dated in the future, a negative
// streak. Such a record is not obeyed - it is noise.
func sanitizeHold(h hold, now time.Time) hold {
	const slack = time.Minute
	if h.Until.After(now.Add(maxHold+slack)) || h.LastFailure.After(now.Add(slack)) || h.Failures < 0 {
		return hold{}
	}
	return h
}

// writeHoldFile publishes state write-then-rename.
func writeHoldFile(path string, state holdFile) error {
	state.Schema = holdsSchema
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return writeFileAtomic(path, data)
}

// prune drops records that no longer say anything, so the file does not
// grow with every community that ever failed once.
func (st *holdFile) prune(now time.Time) {
	for name, rec := range st.Communities {
		if rec.empty(now) {
			delete(st.Communities, name)
		}
	}
	if len(st.Communities) == 0 {
		st.Communities = nil
	}
	if st.Host.empty(now) {
		st.Host = hold{}
	}
}

// overlay lays mine's holds over st wherever mine's ends later.
func (st holdFile) overlay(mine holdFile, now time.Time) holdFile {
	out := st.clone()
	if mine.Host.activeAt(now) && mine.Host.Until.After(out.Host.Until) {
		out.Host = mine.Host
	}
	for name, rec := range mine.Communities {
		if rec.activeAt(now) && rec.Until.After(out.get(name).Until) {
			out.set(name, rec)
		}
	}
	return out
}

func (st holdFile) clone() holdFile {
	out := holdFile{Schema: st.Schema, Host: st.Host}
	for name, rec := range st.Communities {
		out.set(name, rec)
	}
	return out
}

// ceilSecond rounds t UP to a whole second, so every rendering of a hold -
// the clock time a notice prints and the retry_at a document carries - names
// the same moment, and none names one before lmm will actually ask (review
// F12).
func ceilSecond(t time.Time) time.Time {
	if t.Truncate(time.Second).Equal(t) {
		return t
	}
	return t.Truncate(time.Second).Add(time.Second)
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
