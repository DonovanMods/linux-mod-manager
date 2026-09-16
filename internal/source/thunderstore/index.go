// Package thunderstore: this file is the index's LIFECYCLE - when it is
// fetched, how the fetched document becomes the two files on disk, and the
// source.LocalIndexSource seam a frontend drives it through.
//
// The decode is STREAMING. Thunderstore's largest community is 329 MB
// decoded and 34.6 MB on the wire; read one package at a time and the peak
// heap is 26 MB (measured), which is the difference between this being
// possible and not.
package thunderstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
)

// indexTTL is how long an index is served without asking upstream anything
// at all. Six hours, steamworkshop's positiveTTL and for its reason: a
// package the user is about to install did not change in the last six
// hours, and if it did, the next `lmm update` catches it.
const indexTTL = 6 * time.Hour

// progressEvery is how many packages pass between progress ticks. The
// largest community is 50,707 packages, so this is roughly 50 ticks - often
// enough that a slow disk still looks alive, rare enough that the sink is
// not the bottleneck.
const progressEvery = 1000

// wirePackage is one package as Thunderstore serves it. Only the fields the
// index keeps are declared; everything else (uuid4, rating_score,
// downloads, ...) is skipped by the decoder without allocating.
type wirePackage struct {
	Name         string        `json:"name"`
	FullName     string        `json:"full_name"`
	Owner        string        `json:"owner"`
	DateUpdated  string        `json:"date_updated"`
	IsDeprecated bool          `json:"is_deprecated"`
	HasNSFW      bool          `json:"has_nsfw_content"`
	Categories   []string      `json:"categories"`
	Versions     []wireVersion `json:"versions"`
}

// wireVersion is one version of a package. Thunderstore orders versions
// newest-first, which is the order everything downstream relies on.
type wireVersion struct {
	VersionNumber string   `json:"version_number"`
	Description   string   `json:"description"`
	Dependencies  []string `json:"dependencies"`
	DateCreated   string   `json:"date_created"`
	WebsiteURL    string   `json:"website_url"`
	FileSize      int64    `json:"file_size"`
}

// IndexStatus implements source.LocalIndexSource: what is cached for this
// community, if anything. It runs the FULL validity check - a frontend
// asking "what have I got" is exactly where a truncated index should be
// reported as absent rather than as an index - except once the resident
// copy has already been loaded out of those same bytes, when there is
// nothing left to re-check (see usable).
func (s *Source) IndexStatus(ctx context.Context, community string) (source.IndexStatus, error) {
	if err := ctx.Err(); err != nil {
		return source.IndexStatus{}, err
	}
	if err := validateCommunity(community); err != nil {
		return source.IndexStatus{}, err
	}
	wm, _, ok := s.usable(community)
	if !ok {
		return source.IndexStatus{GameID: community}, nil
	}
	return s.statusFor(community, wm), nil
}

// RefreshIndex implements source.LocalIndexSource: bring the index up to
// date, skipping the TTL when force is set. It still sends
// If-Modified-Since either way - a 304 is the correct answer to "is this
// current?" and costs nothing.
//
// A failure over an index that is still usable returns that index's status
// ALONGSIDE the error: the caller serves the stale copy and reports the
// failure as a warning (source.LocalIndexSource's contract).
func (s *Source) RefreshIndex(ctx context.Context, community string, force bool, progress source.IndexProgressFunc) (source.IndexStatus, error) {
	if err := validateCommunity(community); err != nil {
		return source.IndexStatus{}, err
	}
	wm, _, present, err := s.ensureIndex(ctx, community, force, progress)
	if !present {
		return source.IndexStatus{GameID: community}, err
	}
	return s.statusFor(community, wm), err
}

// statusFor renders a watermark as the seam's status document.
func (s *Source) statusFor(community string, wm watermark) source.IndexStatus {
	fetched := time.Unix(wm.FetchedAt, 0).UTC()
	return source.IndexStatus{
		GameID:    community,
		Present:   true,
		Packages:  wm.Packages,
		FetchedAt: fetched,
		Bytes:     s.store.footprint(community),
		Stale:     s.now().Sub(fetched) >= indexTTL,
	}
}

// ensureIndex is the whole refresh policy in one place:
//
//   - no usable index: fetch and build, and a failure is ErrIndexUnavailable.
//   - usable, within the TTL, not forced: no request at all.
//   - usable, past the TTL (or forced): a conditional GET. 304 stamps the
//     watermark and nothing else moves; 200 rebuilds both files.
//   - usable, and the refresh FAILED: the stale index is returned as
//     present, with the error, for the caller to surface as a warning.
//
// present reports whether there is a usable index AFTER the call, which is
// the only thing a caller needs to decide between serving and failing.
// rows is index.json already parsed when getting to this answer required
// parsing it, and nil otherwise (T1 review #4).
func (s *Source) ensureIndex(ctx context.Context, community string, force bool, progress source.IndexProgressFunc) (wm watermark, rows []indexRow, present bool, err error) {
	if err := ctx.Err(); err != nil {
		return watermark{}, nil, false, err
	}
	// Per community rather than per source: a cold build for one community
	// must not block a search of another that is already cached, and two
	// searches of the SAME community must not both download 34 MB.
	lock := s.communityLock(community)
	lock.Lock()
	defer lock.Unlock()

	// The warm path answers before any lock file is touched: an index
	// inside its TTL is a pure read of three files, and making the most
	// common call in the source contend with anything at all would be a
	// cost paid on every query to fix a case that arises on a rebuild.
	if current, rows, usable := s.usable(community); usable && !force && s.now().Sub(time.Unix(current.FetchedAt, 0)) < indexTTL {
		return current, rows, true, nil
	}

	// From here a rebuild is possible, so the CROSS-PROCESS lock applies
	// (T1 review #2): `lmm serve` and a `lmm search` in a terminal are two
	// processes sharing one TTL, and two of their commits interleaving
	// leaves one build's index.json addressing the other's packages.jsonl.
	unlock, lockErr := s.store.lockCommunity(ctx, community)
	if lockErr != nil {
		if current, rows, usable := s.usable(community); usable {
			// Another lmm is rebuilding and we have a copy: serve it, and
			// report the contention as the warning a stale index gets.
			return current, rows, true, lockErr
		}
		return watermark{}, nil, false, indexUnavailable(community, lockErr)
	}
	defer unlock()

	// Re-read UNDER the lock. The process we waited for has very likely
	// just built exactly the index we were about to fetch, and the whole
	// value of waiting is not downloading it a second time.
	current, rows, usable := s.usable(community)
	if usable && !force && s.now().Sub(time.Unix(current.FetchedAt, 0)) < indexTTL {
		return current, rows, true, nil
	}

	refreshed, err := s.refresh(ctx, community, current, usable, progress)
	if err != nil {
		if usable {
			// A failed refresh over a usable index is not an error: serve
			// what is on disk and let the caller decide how loudly to say
			// the copy is old.
			return current, rows, true, err
		}
		return watermark{}, nil, false, err
	}
	// A rebuild published new bytes, so whatever was parsed above describes
	// the index that has just been replaced.
	return refreshed, nil, true, nil
}

// refresh performs the conditional GET and, when the document has changed,
// the streaming rebuild.
//
// A COLD build that nobody asked for by name - the one Search, a package
// read or an update check does for itself - is announced as a
// source.Notice on ctx (#360 §2.7, T1 review #8): it is the one-time wait of
// a few seconds that otherwise explains itself to nobody, whichever command
// happened to trigger it. A caller that passed its own progress function
// asked for the build and reports it that way, so it is not announced
// twice; a refresh over a usable index is a conditional request that costs
// nothing worth explaining.
//
// A HELD host or community (hold.go) is refused here, before anything is
// sent or announced - a "building the index" line followed at once by "not
// asking" would be two lines for one refusal. The retry transport refuses a
// held host too, for any request that does not come through here. What
// this fetch finds out about the community itself - a 404, a document that
// is not a package list - is recorded against it, and a good answer clears
// it.
func (s *Source) refresh(ctx context.Context, community string, current watermark, usable bool, progress source.IndexProgressFunc) (watermark, error) {
	if held, scope, open := s.holds.active(community); open {
		source.Notify(ctx, source.Notice{Kind: source.NoticeSuspended, Source: serviceName, GameID: scope, Until: held.Until})
		return watermark{}, indexUnavailable(community, &source.RetryLaterError{
			Source: serviceName, GameID: scope, Until: held.Until, Reason: held.Reason,
		})
	}
	wm, err := s.fetchAndBuild(ctx, community, current, usable, progress)
	var doc *documentError
	switch {
	case err == nil:
		s.holds.succeeded(community)
	case errors.As(err, &doc):
		s.holds.failed(community, doc.Error())
	}
	return wm, err
}

// fetchAndBuild is refresh's request and rebuild.
func (s *Source) fetchAndBuild(ctx context.Context, community string, current watermark, usable bool, progress source.IndexProgressFunc) (watermark, error) {
	ifModifiedSince := ""
	if usable {
		ifModifiedSince = current.LastModified
	}
	announce := progress == nil && !usable
	started := s.now()
	if announce {
		source.Notify(ctx, source.Notice{Kind: source.NoticeIndexBuilding, Source: serviceName, GameID: community})
	}
	tick := progress
	if tick == nil {
		tick = func(string, string, int64) {}
	}
	tick(source.FetchPhaseStarted, fmt.Sprintf("fetching the Thunderstore index for %s", community), 0)

	resp, err := s.client.fetchCommunity(ctx, community, ifModifiedSince)
	if err != nil {
		// A hold is its own sentence: the request plumbing around it
		// ("fetching ...: executing request to ...") says nothing a user
		// needs about a request lmm decided not to send.
		var later *source.RetryLaterError
		if errors.As(err, &later) {
			err = later
		}
		return watermark{}, indexUnavailable(community, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotModified {
		stamped, err := s.store.stampWatermark(community, current, s.now())
		if err != nil {
			return watermark{}, indexUnavailable(community, err)
		}
		tick(source.FetchPhaseDone, fmt.Sprintf("the %s index is current (%d packages)", community, stamped.Packages), 0)
		return stamped, nil
	}

	wm, err := s.build(ctx, community, resp, tick)
	if err != nil {
		return watermark{}, err
	}
	tick(source.FetchPhaseDone, fmt.Sprintf("indexed %d packages for %s", wm.Packages, community), 0)
	if announce {
		source.Notify(ctx, source.Notice{
			Kind: source.NoticeIndexBuilt, Source: serviceName, GameID: community,
			Packages: wm.Packages, Elapsed: s.now().Sub(started),
		})
	}
	return wm, nil
}

// build streams the response into the two files and publishes them.
//
// One package is decoded, projected and written at a time: nothing here
// ever holds the whole document, or even a whole community's worth of
// records. The only thing that accumulates is the index row table, which is
// 2.6% of the decoded size.
func (s *Source) build(ctx context.Context, community string, resp *http.Response, tick source.IndexProgressFunc) (watermark, error) {
	b, err := newBuilder(s.store.dir(community))
	if err != nil {
		return watermark{}, indexUnavailable(community, err)
	}
	defer b.abort() // no-op once commit has renamed everything into place

	counter := &countingReader{r: resp.Body}
	dec := json.NewDecoder(counter)
	if tok, err := dec.Token(); err != nil || tok != json.Delim('[') {
		if err != nil && !isDocumentFault(err) {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return watermark{}, ctxErr
			}
			return watermark{}, indexUnavailable(community, fmt.Errorf("reading the package index: %w", err))
		}
		return watermark{}, indexUnavailable(community, &documentError{err: fmt.Errorf("the package index is not a JSON array")})
	}
	for n := 0; dec.More(); n++ {
		// Checked per package rather than per read: a cancelled search or a
		// closed browser tab must stop a 34 MB build promptly, and the
		// staging files are removed by the deferred abort above.
		if err := ctx.Err(); err != nil {
			return watermark{}, err
		}
		var p wirePackage
		if err := dec.Decode(&p); err != nil {
			// A cancellation that lands INSIDE Decode - which it does on
			// any package larger than one read - is a cancellation, not an
			// index that could not be built (T1 review #5). Reported
			// exactly as the loop's own guard above reports it, so a
			// closed browser tab and a dead upstream are never the same
			// error.
			if ctxErr := ctx.Err(); ctxErr != nil {
				return watermark{}, ctxErr
			}
			err = fmt.Errorf("decoding package %d: %w", n+1, err)
			if isDocumentFault(err) {
				err = &documentError{err: err}
			}
			return watermark{}, indexUnavailable(community, err)
		}
		rec, row := project(p)
		if row.FullName == "" {
			continue // a package with no identity indexes nothing
		}
		if err := b.add(rec, row); err != nil {
			return watermark{}, indexUnavailable(community, err)
		}
		if (n+1)%progressEvery == 0 {
			tick(source.FetchPhaseProgress, fmt.Sprintf("indexed %d packages", n+1), counter.n)
		}
	}

	wm, err := b.commit(watermark{LastModified: resp.Header.Get("Last-Modified"), FetchedAt: s.now().Unix()})
	if err != nil {
		return watermark{}, indexUnavailable(community, err)
	}
	s.dropResident(community)
	return wm, nil
}

// project splits one wire package into the record packages.jsonl keeps and
// the row index.json searches. The offsets are filled in by builder.add.
func project(p wirePackage) (packageRecord, indexRow) {
	rec := packageRecord{
		FullName:    p.FullName,
		DateUpdated: p.DateUpdated,
		Categories:  p.Categories,
		Deprecated:  p.IsDeprecated,
		NSFW:        p.HasNSFW,
	}
	row := indexRow{
		FullName:    p.FullName,
		Categories:  p.Categories,
		DateUpdated: p.DateUpdated,
		Deprecated:  p.IsDeprecated,
		NSFW:        p.HasNSFW,
	}
	if len(p.Versions) > 0 {
		latest := p.Versions[0]
		rec.Description = latest.Description
		rec.WebsiteURL = latest.WebsiteURL
		row.Description = latest.Description
		row.LatestVersion = latest.VersionNumber
	}
	rec.Versions = make([]versionRow, 0, len(p.Versions))
	for _, v := range p.Versions {
		rec.Versions = append(rec.Versions, versionRow{
			Version:      v.VersionNumber,
			FileSize:     v.FileSize,
			DateCreated:  v.DateCreated,
			Dependencies: v.Dependencies,
		})
	}
	return rec, row
}

// usable reports the watermark of an index that can be searched right now,
// AND the rows it had to parse to say so.
//
// The full check runs once per index generation per process: once the
// resident copy has been loaded out of these bytes, re-reading index.json
// on every query to re-answer the same question would buy nothing - and
// that is the case rows comes back nil for, because nothing was read.
// Otherwise the rows verify parsed are handed on to residentFor rather
// than thrown away and re-parsed (T1 review #4).
func (s *Source) usable(community string) (watermark, []indexRow, bool) {
	wm, ok := s.store.state(community)
	if !ok {
		return watermark{}, nil, false
	}
	if s.residentMatches(community, wm.FetchedAt) {
		return wm, nil, true
	}
	return s.store.verify(community)
}

// communityLock returns the per-community build lock, creating it once.
func (s *Source) communityLock(community string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.locks == nil {
		s.locks = make(map[string]*sync.Mutex)
	}
	lock, ok := s.locks[community]
	if !ok {
		lock = &sync.Mutex{}
		s.locks[community] = lock
	}
	return lock
}

// indexUnavailable wraps a failure that leaves the caller with no index.
//
// BOTH verbs are %w (T1 review #5): the sentinel is what a frontend
// branches on, and the cause is what tells a maintainer - or a test -
// which failure it actually was. Flattening the cause to text with %v left
// callers unable to tell an unreachable upstream from an unwritable cache
// directory without matching on a sentence.
//
// A cause that already IS the sentinel (a host lmm is refusing to ask) is
// not wrapped in it a second time, which only repeated "source index is
// unavailable" in the sentence a user reads.
func indexUnavailable(community string, err error) error {
	if errors.Is(err, ErrIndexUnavailable) {
		return fmt.Errorf("source %q: the %s index could not be built: %w", sourceID, community, err)
	}
	return fmt.Errorf("source %q: the %s index could not be built: %w: %w", sourceID, community, err, ErrIndexUnavailable)
}

// countingReader counts the bytes actually pulled off the wire, for the
// progress ticks. Not concurrent: it is read by exactly one decoder on one
// goroutine.
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// documentError is a failure that belongs to one community's DOCUMENT - it
// is missing, or it is not a package list - rather than to the host or the
// transfer, and so holds only that community (T3 review F9).
type documentError struct{ err error }

func (e *documentError) Error() string { return e.err.Error() }
func (e *documentError) Unwrap() error { return e.err }

// isDocumentFault reports whether a decode failure is the document's own
// shape. A read that failed - a stall, a dropped connection, a body cut
// short - says nothing about the document, and neither does a document too
// large to accept, which is lmm's own ceiling.
func isDocumentFault(err error) bool {
	var syntax *json.SyntaxError
	var typ *json.UnmarshalTypeError
	return errors.As(err, &syntax) || errors.As(err, &typ)
}
