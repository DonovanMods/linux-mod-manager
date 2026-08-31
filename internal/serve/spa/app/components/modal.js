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
 */
export function Modal({ kind, title, onClose, footer, children }) {
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
    // walks the page underneath instead of the dialog.
    panelRef.current?.focus();
    return () => document.removeEventListener("keydown", handleKeyDown);
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
