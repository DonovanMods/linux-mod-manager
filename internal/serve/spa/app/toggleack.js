// toggleack.js - the ledger of enable/disable requests behind every toggle
// control in this application (issue 432).
//
// Enabling a mod genuinely deploys its files (core.Service.EnableMod ->
// installer.Install), so the wait between the click and the new truth is
// real work and cannot be optimised away. What was wrong is that the UI
// spent that whole wait looking exactly as it had before, so people clicked
// again. This module is the one place that remembers "you asked for this,
// it has not happened yet": a control renders the REQUESTED value while its
// mod has an entry here, and the server's value otherwise.
//
// ONE LEDGER, IN THE STORE. Entries live in state.toggleRequests, keyed by
// the context (game, profile) and the mod key, and every surface - the
// library row, the batch bar, the slide-over, the full mod page - reads the
// same entry. That is what makes "one live request per mod" true across
// surfaces rather than per component: a mod the batch still owes a job is
// pending on its slide-over too, and open() refuses a second request for a
// mod that already has one.
//
// SETTLED BY ITS OWN JOB, NEVER BY AGREEMENT. An entry ends only on the
// terminal state of the job started FOR IT - failed, or succeeded and then
// re-read. The server merely agreeing with an entry settles nothing: a
// batch row that already reads the batch's target value is still owed a
// job, and a request whose job has not run can be overtaken by one that
// has. Both of those once left a row pending for the rest of the session.
//
// RE-READ MEANS A READ WAS WRITTEN. "Re-read" is the library document -
// the one every toggle renders from - committed to the store by a read
// issued after the job's end, for the entry's own context
// (slicefence.js stamps every claim, so "issued after" is exact). Not "the
// read the ending started has finished": a read can finish having been
// superseded by a newer one and written nothing, and the control would
// then show the document from before the job until the newer read landed.
// A read committed with an error settles the entry too, with a toast: the
// current state could not be read.
//
// Transitions are driven by events (main.js), not by polling a render:
//
//   state       what it means                   leaves when                             to
//   ----------  ------------------------------  --------------------------------------  ----------
//   requested   the click is recorded; no job   bind: the start answered with a job id  running
//               id yet - the start's POST is    drop: the start was refused or failed   (removed)
//               in flight, or a batch has not     (an HTTP error, a network error); the
//               reached this row                  start's toast, or the batch's tally,
//                                                 says so
//                                               unanswered: the start's deadline        unanswered
//                                                 (api.js#jobStartDeadlineMillis)
//                                                 passed with no answer; toasted as "did
//                                                 not answer - may or may not have been
//                                                 applied"
//   running     bound to its own job, whose     ended(failed): its job_done - live, or  (removed)
//               end has not been heard            found by reconciliation; onJobDone
//                                                 surfaces it (toast, or the inline
//                                                 "Failed:"), and the control goes
//                                                 back to the document the job did
//                                                 not change
//                                               ended(succeeded): the same, succeeded   confirming
//                                               ended(lost): reconciliation found the   confirming
//                                                 server no longer knows the job, or
//                                                 could not ask; always toasted
//   confirming  its job succeeded, or was       read: a library read issued after the   (removed)
//               lost; holding the asked-for       job's end is committed for this
//               value until a read taken after    context; the control then shows it,
//               that end lands                    whatever it says
//                                               unread: that read was committed as an   (removed)
//                                                 error; toasted as "could not be read",
//                                                 and the control shows the last
//                                                 document the server sent
//   unanswered  its start was never answered,   read: as confirming, for a read issued  (removed)
//               so nothing says whether the       after the start's deadline
//               change happened; holding the    unread: as confirming                   (removed)
//               asked-for value until a read
//               taken after the deadline lands
//
// Nothing but a start's own answer leaves requested, and that answer can
// only arrive once: bind() and drop() act on a requested entry alone, and an
// answer that comes after the deadline is never read at all (the request is
// aborted). So a late answer can neither bring an unanswered entry back nor
// settle it a second time. A job that late start did create is simply not
// this entry's: its end re-reads the documents like any other job's, and
// the control follows those.
//
// bind() on a job that has ALREADY ended applies that ending on the spot,
// so the order the two arrive in never matters - whether the end came as a
// job_done frame before the POST that names the job was read, or only as a
// terminal row in a reconnect's snapshot (main.js#knownEnding).
//
// Why each state is left, by construction rather than by luck:
//
//   - requested: a start is one request, under a deadline, so it is
//     answered, fails or is abandoned - there is no fourth outcome. A batch
//     reaches every row because each row ahead of it ends its wait: on a
//     failed or unanswered start, or on one of its job's endings below.
//   - running: the job ends while the activity stream is connected
//     (job_done), or while it is not - and then the reconnect's snapshot
//     reconciles it (activity.js), asking GET /api/v1/jobs/{id} about any
//     job the snapshot does not carry. A job the server cannot account for
//     is lost. The stream always comes back: EventSource retries on its own,
//     and sse.js#followActivity reopens it when the browser gives up.
//   - confirming, unanswered: the ending (or the start's deadline) begins a
//     read of the library, and a read written - a document or an error -
//     ends the hold. A superseded read ends nothing: the read that
//     superseded it was issued later still, so its commit is the one that
//     counts.
//
// An entry for a context nobody is looking at still settles on a read of
// its own context; it simply is not rendered until that context is on
// screen again.

/** modToggleOrigin is the stable origin every enable/disable control for one
 * mod shares - "mod:{source}/{id}:toggle" (modrows.js#modOriginPattern).
 *
 * Stable across the DIRECTION of the toggle, which is the whole point: an
 * origin keyed on "enable" vs "disable" flips the instant the job succeeds,
 * so the next render looks up an origin the job was never bound to. That was
 * a real bug once (TestE2E_SlideOver_EnableDisableMorphsInline) and this
 * function is what keeps its fix in one place. */
export function modToggleOrigin(sourceID, modID) {
  return `mod:${sourceID}/${modID}:toggle`;
}

/** pendingToggleLabel is what an in-flight toggle says about itself, in the
 * same present participle progress.js#mutationKindLabels uses for the job
 * once it has actually started ("Enabling", "Disabling") - so the sentence
 * the row shows before the job exists and the one it shows after are the
 * same words, not two different vocabularies for one operation. */
export function pendingToggleLabel(want) {
  return want ? "Enabling…" : "Disabling…";
}

/** toggleRequestFor is the request pending for modKey ("sourceID:modID") in
 * the context on screen, or undefined when there is none. What a control
 * renders from: `want` is the value the user asked for. */
export function toggleRequestFor(state, modKey) {
  return state.toggleRequests?.[ledgerKey(state.route ?? {}, modKey)];
}

/** ledgerKey is an entry's key: the context and the mod, unambiguously
 * joined (a profile name may contain any separator a string could use). */
function ledgerKey(context, modKey) {
  return JSON.stringify([context.game ?? "", context.profile ?? "", modKey]);
}

/** toggleDocument is the store slice every toggle renders its value from. */
const toggleDocument = "mods";

let requestSeq = 0;

/**
 * createToggleLedger is the ledger's only writer, over `store`. One per
 * application.
 *
 *   - `endingOf(jobID)` answers with the ending main.js recorded for a job -
 *     {summary, after, lost}, `after` being the fence's mark at that end -
 *     or undefined while it has none.
 *   - `fence` is main.js's slice fence (slicefence.js), which says what was
 *     written, and when it was asked for.
 *   - `onUnread(entry, reason)` hears an entry that settled without a read:
 *     the current state could not be read, and `reason` says why.
 */
export function createToggleLedger(store, { endingOf, fence, onUnread }) {
  const entries = () => store.get().toggleRequests ?? {};
  // holds is every entry waiting on a read, by request: {entry, phase,
  // after}. Kept here rather than in the store, because nothing renders
  // it.
  const holds = new Map();

  const inContext = (entry, route) =>
    ledgerKey(route ?? {}, entry.modKey) === entry.key;

  // lastRead is the newest library document written, and the context it
  // was read for: the route on screen when it was committed, which the
  // fence's route check guarantees is the one it was asked for. A binding
  // that arrives after its job's end may find its read already here.
  let lastRead;
  fence.onCommit((key, write) => {
    if (key !== toggleDocument) return;
    const route = store.get().route ?? {};
    lastRead = { ...write, route };
    for (const waiting of [...holds.values()]) {
      if (write.stamp > waiting.after && inContext(waiting.entry, route))
        release(waiting, write.error);
    }
  });

  // Every write names the REQUEST it is about, never just the key: an
  // entry that has since been replaced by a newer request for the same mod
  // must not receive news about the older one.
  function current(entry) {
    const stored = entries()[entry.key];
    return stored?.request === entry.request ? stored : undefined;
  }

  function update(entry, patch) {
    const stored = current(entry);
    if (!stored) return undefined;
    const next = { ...stored, ...patch };
    store.set({ toggleRequests: { ...entries(), [entry.key]: next } });
    return next;
  }

  function remove(entry) {
    if (!current(entry)) return;
    const next = { ...entries() };
    delete next[entry.key];
    store.set({ toggleRequests: next });
  }

  // A failed job changed nothing the document on screen does not already
  // say, so its request goes at once. A succeeded job changed the mod, and a
  // lost one may have: only a read taken after its end can say what is true
  // now, and until one lands the control keeps what was asked rather than
  // flick back to the document from before the job.
  function settle(entry, ending) {
    if (ending.summary.state === "failed" && !ending.lost) {
      remove(entry);
      return;
    }
    hold(entry, "confirming", ending.after);
  }

  // hold moves entry to `phase` until a read stamped above `after` is
  // written for its context - or already has been, which a late binding
  // finds.
  function hold(entry, phase, after) {
    const held = update(entry, { phase });
    if (!held) return;
    const waiting = { entry: held, phase, after };
    holds.set(held.request, waiting);
    if (lastRead?.stamp > after && inContext(held, lastRead.route))
      release(waiting, lastRead.error);
  }

  // release ends waiting's hold: the control shows the document from here
  // on, and `error`, when there is one, is why it could not be re-read.
  function release(waiting, error) {
    const { entry, phase } = waiting;
    if (holds.get(entry.request) !== waiting) return;
    holds.delete(entry.request);
    if (current(entry)?.phase !== phase) return;
    remove(entry);
    if (error) onUnread(entry, error);
  }

  return {
    /** open records a request for each of `mods` ({key, name}) in
     * `context`, asking for `want`, and returns the entries it opened. A
     * mod that already has a live request is skipped: one per mod. */
    open(context, mods, want) {
      const next = { ...entries() };
      const opened = [];
      for (const mod of mods) {
        const key = ledgerKey(context, mod.key);
        if (next[key]) continue;
        const entry = {
          key,
          modKey: mod.key,
          name: mod.name,
          want,
          request: ++requestSeq,
          phase: "requested",
          jobID: null,
        };
        next[key] = entry;
        opened.push(entry);
      }
      if (opened.length > 0) store.set({ toggleRequests: next });
      return opened;
    },

    /** bind moves a requested entry onto its own job - and straight on to
     * that job's ending, when the ending is already known. */
    bind(entry, jobID) {
      if (current(entry)?.phase !== "requested") return;
      const running = update(entry, { phase: "running", jobID });
      // endingOf may itself end the job (main.js#knownEnding), which
      // settles this entry through ended() - hence the second look.
      const ending = endingOf(jobID);
      if (ending && current(running)?.phase === "running")
        settle(running, ending);
    },

    /** drop removes a requested entry whose start made no job. */
    drop(entry) {
      if (current(entry)?.phase === "requested") remove(entry);
    },

    /** unanswered ends a requested entry whose start went unanswered past
     * its deadline: nothing says whether the change happened, so the entry
     * keeps what was asked until a read stamped above `after` - the fence's
     * mark at the deadline - is written, and the control then shows it. */
    unanswered(entry, after) {
      if (current(entry)?.phase === "requested")
        hold(entry, "unanswered", after);
    },

    /** ended applies a job's ending to every entry running on it, and
     * returns those entries. */
    ended(jobID, ending) {
      const running = Object.values(entries()).filter(
        (entry) => entry.phase === "running" && entry.jobID === jobID,
      );
      for (const entry of running) settle(entry, ending);
      return running;
    },

    /** runningJobIDs is every job an entry is waiting on - what a
     * reconnect has to reconcile. */
    runningJobIDs() {
      return Object.values(entries())
        .filter((entry) => entry.phase === "running")
        .map((entry) => entry.jobID);
    },
  };
}
