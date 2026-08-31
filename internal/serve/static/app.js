/*
 * app.js: the ONE progressive-enhancement script for lmm serve
 * (docs/plans/2026-08-30-serve-impl.md Task 10; WEBUI.md: JS as
 * progressive enhancement, no framework, no inline JS, no application
 * logic hidden in frontend behavior). Every page and every mutation
 * already works with this file entirely absent - see the no-JS tests in
 * Units 4/5 - so everything below only makes the same outcomes visible
 * sooner, never changes what happens.
 *
 * Two things are enhanced, matching the two data hooks the templates
 * carry:
 *
 *   1. A running job's page ([data-job-id][data-job-state="running"]):
 *      subscribes to its SSE stream and shows a live one-line "what's
 *      happening now" summary (data-job-live), using only the two fields
 *      every core event generalizes (phase, mod name) - it deliberately
 *      does NOT reconstruct the full per-event-type detail text
 *      pages_jobs.go's jobEventLines derives (a Go type switch per event
 *      kind), since duplicating that here would be exactly the kind of
 *      frontend-hidden application logic WEBUI.md rules out, and would
 *      silently drift the moment core gains a new event field. The full,
 *      correct event log is what step 2 below fetches once the job ends.
 *
 *   2. A confirm form ([data-js-enhance="confirm"]): submitting it (by
 *      any of its three buttons) is intercepted and sent via fetch
 *      instead of a full navigation. Whatever page the server renders in
 *      response - the started job, a re-plan confirm page, an error page,
 *      anything - replaces the current page's <main> in place, and is
 *      then re-scanned by this same script, so a fresh confirm page keeps
 *      working and a job page in the response starts streaming its own
 *      progress immediately. This is also how a running job's page swaps
 *      itself to the final result: on the stream's terminal "done" frame,
 *      the SAME page is re-fetched and swapped in, which is exactly the
 *      complete, server-rendered outcome a manual reload would show -
 *      never a client-reconstructed approximation of it.
 *
 * A dropped or permanently failed SSE connection falls back to the
 * always-present manual "Refresh" link job.gohtml renders while running
 * (and, with JS off entirely, the <noscript> meta refresh) - EventSource
 * already retries a transient drop on its own, and this script adds no
 * competing retry logic of its own for either case.
 */
(function () {
  "use strict";

  if (!window.fetch || !window.EventSource) {
    // No custom fallback needed: every page already works without this
    // script at all, so simply not running it degrades to that.
    return;
  }

  // JOB_EVENT_TYPES are the SSE frame names a job stream ever sends for a
  // core event (internal/core/events.go's EventType() values), plus the
  // terminal "done" frame sse.go defines. EventSource requires each event
  // name to be subscribed individually; there is no generic "any named
  // event" handler.
  var JOB_EVENT_TYPES = [
    "step",
    "download",
    "mod",
    "hook",
    "warning",
    "merge",
    "update_check",
  ];

  // startJobStream subscribes to root's job and keeps data-job-live
  // updated until the job ends, then swaps root's page to the final
  // result. root is the element carrying data-job-id / data-job-state, as
  // rendered by job.gohtml.
  function startJobStream(root) {
    var jobID = root.getAttribute("data-job-id");
    var live = root.querySelector("[data-job-live]");
    if (!jobID || !live) return;

    var seen = 0;
    var es = new EventSource(
      "/api/v1/jobs/" + encodeURIComponent(jobID) + "/events",
    );

    function showActivity(phase, modName) {
      seen++;
      var text = phase ? phase.replace(/_/g, " ") : "working";
      if (modName) text += " - " + modName;
      text += " (" + seen + " event" + (seen === 1 ? "" : "s") + " so far)";
      live.textContent = text;
    }

    JOB_EVENT_TYPES.forEach(function (type) {
      es.addEventListener(type, function (event) {
        try {
          var data = JSON.parse(event.data);
          showActivity(data.phase, data.mod_name);
        } catch (err) {
          seen++;
        }
      });
    });

    es.addEventListener("done", function () {
      es.close();
      swapCurrentPage();
    });

    // No onerror handling beyond letting EventSource retry on its own: see
    // the file header. A permanently failed connection leaves data-job-live
    // showing its last update, which is honest (that WAS the last thing
    // seen), with the manual Refresh link still there to act on.
  }

  // swapCurrentPage re-fetches the page currently displayed and replaces
  // #main-content with the server's fresh render of it - used once a job's
  // stream reports it is done, so the page becomes the real result exactly
  // as a manual reload would show it.
  function swapCurrentPage() {
    fetch(location.pathname + location.search)
      .then(function (res) {
        return res.text();
      })
      .then(function (html) {
        replaceMain(html, null);
      })
      .catch(function () {
        // The manual Refresh link (still on the page - see the file
        // header) and, with JS off, the meta refresh both still work.
      });
  }

  // replaceMain parses html (a complete page this server rendered) and
  // replaces the current document's #main-content with the parsed page's
  // own #main-content's children, keeping the title in sync and
  // re-running init on the new content so it starts working immediately.
  // url, when given, becomes the visible address via history.replaceState
  // - a confirm submission's result IS a new page (a started job, most
  // often), and the address bar should say so, the same way a plain form
  // submission's redirect would.
  //
  // DOMParser never executes scripts or fires side effects while parsing
  // (html is same-origin content this server itself just rendered - the
  // same trust boundary a normal navigation to that URL already crosses),
  // and moving the parsed nodes in with replaceChildren rather than
  // reserialising them through innerHTML keeps this a DOM-to-DOM move.
  function replaceMain(html, url) {
    var doc = new DOMParser().parseFromString(html, "text/html");
    var next = doc.getElementById("main-content");
    var current = document.getElementById("main-content");
    if (!next || !current) {
      // Not a page shape this script recognises - leave the DOM alone
      // rather than guess.
      return;
    }
    current.replaceChildren.apply(
      current,
      Array.prototype.slice.call(next.childNodes),
    );
    if (doc.title) document.title = doc.title;
    if (url) history.replaceState(null, "", url);
    init(current);
  }

  // enhanceConfirmForm intercepts form's submit and sends it via fetch,
  // swapping in whatever the server renders in response instead of a full
  // navigation. The submitting button's own name/value is included
  // explicitly (confirm.gohtml: "confirm" rides on the buttons, not a
  // hidden field, so a plain `new FormData(form)` alone would drop it),
  // and its formaction (when it names one, e.g. the sync fallback button)
  // is honoured over the form's own action.
  function enhanceConfirmForm(form) {
    if (form.dataset.jsEnhanced) return;
    form.dataset.jsEnhanced = "1";

    form.addEventListener("submit", function (event) {
      var submitter = event.submitter;
      event.preventDefault();

      var action =
        submitter && submitter.hasAttribute("formaction")
          ? submitter.formAction
          : form.action;
      var data = new FormData(form);
      if (submitter && submitter.name) {
        data.append(submitter.name, submitter.value);
      }
      if (submitter) submitter.disabled = true;

      fetch(action, { method: "POST", body: data })
        .then(function (res) {
          return res.text().then(function (html) {
            return { html: html, url: res.url };
          });
        })
        .then(function (result) {
          replaceMain(result.html, result.url);
        })
        .catch(function () {
          // Fall back to a plain navigation that still honors WHICH button
          // was clicked and where it targets: confirm.gohtml keys its real
          // action off the submitter (not a hidden field) and the sync
          // button's own formaction, neither of which a bare form.submit()
          // reads on its own - it always POSTs to form.action with none of
          // the submitter's fields. form.requestSubmit(submitter) looks
          // like the fix (and IS one when called synchronously from a
          // click), but verified manually in a real browser (task-11
          // report) it silently no-ops - no request, no thrown error -
          // when called from this async fetch().catch() continuation
          // instead of directly inside a click handler, which would make
          // the fallback do nothing at all. So mirror both pieces into the
          // form by hand instead and use the plain submit() that already
          // worked.
          if (submitter) submitter.disabled = false;
          form.action = action;
          if (submitter && submitter.name) {
            var hidden = document.createElement("input");
            hidden.type = "hidden";
            hidden.name = submitter.name;
            hidden.value = submitter.value;
            form.appendChild(hidden);
          }
          form.submit();
        });
    });
  }

  // init wires every enhancement found within scope (a Document on first
  // load, or a freshly swapped-in #main-content afterward).
  function init(scope) {
    var jobRoot = scope.querySelector("[data-job-id]");
    if (jobRoot && jobRoot.getAttribute("data-job-state") === "running") {
      startJobStream(jobRoot);
    }
    var forms = scope.querySelectorAll('form[data-js-enhance="confirm"]');
    for (var i = 0; i < forms.length; i++) {
      enhanceConfirmForm(forms[i]);
    }
  }

  document.addEventListener("DOMContentLoaded", function () {
    init(document);
  });
})();
