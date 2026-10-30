// pendingreads.js - Mission Control's slow reads, named while they are out
// (issue 583).
//
// The update check and the verify behind the Health card walk the
// profile's mods against their sources, and on a real setup took from half
// a second to twelve. A card only renders once it has something to say, so
// while they were out that part of the page was simply empty, and the page
// looked frozen. Each read still out now has a line here saying what it is
// doing - "Checking 42 mods for updates (17/42)…" once the server reports
// how far it has got (read_progress frames, internal/serve/read_progress.go)
// - which the card it becomes replaces when it answers.
//
// The lines follow loading.js's two timing rules (nothing for a read that
// answers inside the show delay; a painted line stays its minimum), the
// region is aria-busy while anything in it is still out, and a polite live
// region says when a painted read starts and finishes - never each tick,
// which would talk over everything else on the page. Focus is never moved.

import { html, useEffect, useRef, useState } from "../render.js";
import {
  PENDING_READS,
  pendingReadDone,
  pendingReadLabel,
  progressFor,
  progressFractionOf,
  useLastWhile,
  useShownWhile,
} from "../loading.js";

/** PendingReads renders the in-flight reads' lines and the live region
 * that announces them. `mods` is the library document, when it has
 * answered - the count a line states before the server reports its own. */
export function PendingReads({ state, mods }) {
  const modCount = mods?.mods ? mods.mods.length : null;

  // One hook call per read, always in PENDING_READS' fixed order - the
  // order Preact matches hook state by.
  const rows = PENDING_READS.map((read) => {
    const active = Boolean(state.loading?.[read]);
    const progress = active ? progressFor(state, read) : null;
    const shown = useShownWhile(active);
    const label = useLastWhile(
      active,
      pendingReadLabel(read, { mods: modCount, progress }),
    );
    return {
      read,
      active,
      shown,
      label,
      fraction: progressFractionOf(progress),
      startLabel: pendingReadLabel(read, { mods: modCount }),
    };
  });

  const announcement = useAnnouncement(rows);
  const visible = rows.filter((row) => row.shown);
  const busy = rows.some((row) => row.shown && row.active);

  return html`
    <p
      class="visually-hidden"
      role="status"
      aria-live="polite"
      data-testid="load-announcer"
    >
      ${announcement}
    </p>
    ${
      visible.length > 0 &&
      html`<section
        class="pending-reads"
        aria-busy=${busy ? "true" : "false"}
        aria-label="Still loading"
      >
        <ul class="pending-reads__list">
          ${visible.map(
            (row) => html`
              <li
                key=${row.read}
                class="pending-read"
                data-pending-read=${row.read}
              >
                <div
                  class="job-progress__bar ${row.fraction === null ? "job-progress__bar--indeterminate" : ""}"
                  aria-hidden="true"
                >
                  <div
                    class="job-progress__fill"
                    style=${row.fraction === null ? "" : `width: ${Math.round(row.fraction * 100)}%`}
                  ></div>
                </div>
                <span class="pending-read__text">${row.label}</span>
              </li>
            `,
          )}
        </ul>
      </section>`
    }
  `;
}

/**
 * useAnnouncement is what the live region says: a painted read's start,
 * and its finish once it answers. A read that never painted is never
 * announced either - it was too quick to be worth interrupting anyone for.
 */
function useAnnouncement(rows) {
  const [message, setMessage] = useState("");
  const announced = useRef({});

  useEffect(() => {
    const said = [];
    for (const row of rows) {
      const was = announced.current[row.read] ?? false;
      if (!was && row.shown && row.active) {
        announced.current[row.read] = true;
        said.push(row.startLabel);
      } else if (was && !row.active) {
        announced.current[row.read] = false;
        said.push(pendingReadDone(row.read));
      }
    }
    if (said.length > 0) setMessage(said.join(" "));
  });

  return message;
}
