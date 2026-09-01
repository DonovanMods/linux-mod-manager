// missioncontrol.js - the home route: the top bar, the attention cards, the
// library, and the ?mod= slide-over, composed
// (docs/plans/2026-08-31-serve-spa-design.md §Mission Control).
//
// The library's row join (buildRows) and its filter/sort state live HERE,
// not inside Library, since issue 330: the slide-over's ←/→ stepping needs
// the exact same "current (filtered/sorted) list" the table is showing
// (the design's own words for it, §Slide-over), and a component cannot step
// through state it does not have. Library stays the toolbar/table renderer;
// this component is the one source of "what rows, in what order".

import { html, useMemo, useState } from "../render.js";
import { progressText, mutationLabel } from "../progress.js";
import { navigate, contextPath } from "../router.js";
import {
  buildRows,
  filterRows,
  sortRows,
  runningMutations,
} from "../modrows.js";
import { TopBar } from "./topbar.js";
import { AttentionCards } from "./cards.js";
import { Library } from "./library.js";
import { ModPanel } from "./modpanel.js";

/** goToChooser is the error state's escape hatch: a click, not just advice
 * to edit the URL bar, back to a context that does resolve. */
function goToChooser(e) {
  e.preventDefault();
  navigate("/");
}

export function MissionControl({ state, onThemeChange, actions }) {
  // Hooks run unconditionally, before either early return below - the
  // query the omnibar edits lives here, one level above the top bar (which
  // renders the input) and the library (which filters by it), rather than
  // in either alone. filter/sort moved up from Library in issue 330 for the
  // same reason: ModPanel needs the same ordered list.
  const [query, setQuery] = useState("");
  const [filter, setFilter] = useState("all");
  const [sort, setSort] = useState("load-order");

  const {
    route,
    status,
    games,
    mods,
    updates,
    health,
    conflicts,
    error,
    fetchErrors,
  } = state;

  const rows = useMemo(
    () =>
      buildRows(
        mods?.mods ?? [],
        updates?.updates ?? [],
        health?.result?.findings ?? [],
        conflicts?.conflicts ?? [],
      ),
    [mods, updates, health, conflicts],
  );

  const matched = useMemo(() => {
    const q = (query ?? "").trim().toLowerCase();
    return q ? rows.filter((r) => r.name.toLowerCase().includes(q)) : rows;
  }, [rows, query]);

  const visible = useMemo(
    () => sortRows(filterRows(matched, filter), sort),
    [matched, filter, sort],
  );

  // The mods currently being mutated as far as THIS session's own controls
  // can say (modrows.js#runningMutations) - the library's row-level live
  // indicator, and what tells the header's own line whether a running job
  // has already found a home on a row (issue 330 carry-3).
  const mutations = runningMutations(
    state.jobsIndex,
    state.jobProgress,
    state.origins,
  );

  if (error) {
    return html`
      <header class="app-bar app-bar--minimal">
        <span class="app-bar__brand">LMM</span>
      </header>
      <main class="app-main">
        <p class="app-error">${error}</p>
        <p><a href="/" onClick=${goToChooser}>Choose a different game</a></p>
      </main>
    `;
  }

  if (status === null) {
    return html`<p class="app-booting">Loading&#8230;</p>`;
  }

  // The live line the library's own header carries while a job is running
  // that no row has already claimed (design's "cards show live counts",
  // §Jobs, applied to the surface this unit has running jobs over). A row-
  // claimed job (mutations) shows there instead - showing it twice would be
  // the same fact said in two places, once too many. The kind's own label
  // is ALWAYS present, unlike the raw phase text alone: uninstall/enable/
  // disable report no progress events at all (issue 330 carry-3), so a
  // frame-only line would render nothing for exactly the mutations this
  // unit adds.
  const runningJob = (state.jobsIndex ?? []).find((j) => j.state === "running");
  const rowClaimed =
    runningJob &&
    [...mutations.values()].some((m) => m.jobID === runningJob.id);
  const liveActivity =
    runningJob && !rowClaimed
      ? [
          mutationLabel(runningJob.kind),
          progressText(state.jobProgress?.[runningJob.id]),
        ]
          .filter(Boolean)
          .join(" · ")
      : "";

  return html`
    <div class="mission-control" data-hydrated="true">
      <${TopBar}
        state=${state}
        status=${status}
        games=${games}
        route=${route}
        mods=${mods?.mods}
        query=${query}
        onQueryChange=${setQuery}
        onThemeChange=${onThemeChange}
        actions=${actions}
      />
      ${
        // A re-hydrate failure (main.js's I3-style handling of onJobDone)
        // that happened after this page already loaded - reported here
        // rather than blanking the page, the same rule the four
        // supplementary reads below already follow.
        fetchErrors?.status &&
        html`<p class="app-error">
          ${fetchErrors.status} — showing the last loaded data.
        </p>`
      }
      <main class="mission-control__body">
        <${AttentionCards}
          updates=${updates}
          health=${health}
          conflicts=${conflicts}
          errors=${fetchErrors}
          actions=${actions}
        />
        <${Library}
          mods=${mods}
          visible=${visible}
          filter=${filter}
          sort=${sort}
          onFilterChange=${setFilter}
          onSortChange=${setSort}
          mutations=${mutations}
          liveActivity=${liveActivity}
          query=${query}
          error=${fetchErrors?.mods}
          onRetry=${actions.reloadMods}
        />
      </main>
      ${
        route.mod &&
        html`<${ModPanel}
          modKey=${route.mod}
          contextPath=${contextPath(route.game, route.profile)}
          rows=${rows}
          visible=${visible}
          route=${route}
          state=${state}
          actions=${actions}
        />`
      }
    </div>
  `;
}
