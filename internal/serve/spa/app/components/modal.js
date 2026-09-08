// modal.js - the one modal shell this application has.
//
// The design allows modals to stack AT MOST one deep
// (docs/plans/2026-08-31-serve-spa-design.md §Modals), which is why there
// is a single shell rather than a stack manager: whatever is open is the
// only thing open, so the shell owns the backdrop, the Escape key and the
// outside click without having to ask who else is on screen.
//
// It renders nothing about WHAT is being confirmed - that is the caller's
// children. A confirm-plan, a conflict-overwrite and (later) the reorder
// and profiles modals are all this shell with a different body, which is
// what keeps the pre-flight's "one framework, later units consume it,
// never fork it" true in code rather than only in a comment.

import { html, useEffect, useRef } from "../render.js";

/**
 * The modal shell: a scrim, a labelled dialog panel, a title, the caller's
 * body, and a footer for its actions.
 *
 * kind is stamped as data-kind so a specific modal is addressable (by a
 * test, and by the CSS that wants to size one kind differently) without the
 * shell needing to know what any of them mean.
 *
 * onClose is called by Escape, by a click on the scrim, and by the ✕ -
 * every route out of a modal that isn't one of the caller's own footer
 * actions. A caller with work in flight passes a no-op to hold the modal
 * open; nothing here decides that for it.
 *
 * openerSelector (I2, unit 6 fix wave) names a STABLE, always-mounted
 * control to restore focus to on close, queried FRESH at that moment
 * instead of the activeElement this shell captures at mount. Most callers
 * don't need it - their own opener (the library's "Reorder…" button, a
 * single-mod plan's row control) stays on screen for the modal's whole
 * life, so the captured activeElement is still valid when this unmounts.
 * A caller whose actual clicked element lives inside something that MUST
 * close before (or the instant) the modal opens - the profiles modal's own
 * opener is a menu item inside a dropdown that cannot stay open behind a
 * modal - names a selector for a control that survives that teardown
 * instead (the profile picker's own trigger button, not the menu item).
 */
export function Modal({
  kind,
  title,
  onClose,
  footer,
  children,
  openerSelector,
}) {
  const panelRef = useRef(null);

  // The handler is read through a ref rather than captured, so the listener
  // is attached exactly once for the modal's life: a caller that passes a
  // fresh arrow function each render (every caller, in practice) would
  // otherwise detach and re-attach it on every keystroke.
  const closeRef = useRef(onClose);
  closeRef.current = onClose;

  useEffect(() => {
    function handleKeyDown(e) {
      if (e.key === "Escape") closeRef.current?.();
    }
    document.addEventListener("keydown", handleKeyDown);
    // Focus the panel on open: without it the keyboard focus stays on the
    // control that opened the modal, which is now behind a scrim, and Tab
    // would start walking the page underneath instead of the dialog. This
    // only fixes where Tab STARTS - there is no focus trap (the page behind
    // the panel is neither inert nor aria-hidden), so Tab can still walk out
    // of the dialog once it starts. Full keyboard containment is Unit 8's
    // a11y pass.
    const opener = document.activeElement;
    panelRef.current?.focus();
    return () => {
      document.removeEventListener("keydown", handleKeyDown);
      // Focus returns to the opener on unmount (issue 332), whichever of the
      // shell's own exits fired (Escape, scrim click, ✕, or the caller's own
      // Cancel/Confirm) - every one of them tears this component down, so
      // one cleanup covers all of them rather than each caller having to
      // remember to give focus back itself. openerSelector is queried FRESH
      // here (not at mount) when given - the whole point is that it names a
      // control that only re-exists in its normal place once whatever had
      // to close to let this modal open (a dropdown menu) is gone again,
      // which is exactly the state the page is back in by the time this
      // runs. A guard against a target that is no longer in the document
      // (the row it belonged to was removed by the very mutation this modal
      // just confirmed) - focus() on a detached element is a silent no-op
      // in every browser, but isConnected makes the intent explicit rather
      // than relying on that.
      const target = openerSelector
        ? document.querySelector(openerSelector)
        : opener;
      if (target instanceof HTMLElement && target.isConnected) target.focus();
    };
  }, []);

  return html`
    <div
      class="modal-scrim"
      onClick=${(e) => {
        if (e.target === e.currentTarget) closeRef.current?.();
      }}
    >
      <div
        class="modal"
        data-kind=${kind}
        role="dialog"
        aria-modal="true"
        aria-label=${title}
        tabindex="-1"
        ref=${panelRef}
      >
        <div class="modal__head">
          <p class="modal__title">${title}</p>
          <button
            type="button"
            class="modal__close"
            aria-label="Close"
            onClick=${() => closeRef.current?.()}
          >
            ✕
          </button>
        </div>
        <div class="modal__body">${children}</div>
        ${footer && html`<div class="modal__footer">${footer}</div>`}
      </div>
    </div>
  `;
}
