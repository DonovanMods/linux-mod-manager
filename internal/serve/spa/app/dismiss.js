// dismiss.js - the one rule every dropdown in this application obeys: a
// click outside it, or Escape, closes it and gives the keyboard back.
//
// Extracted from topbar.js when the away-from-home routes (Setup, the full
// mod page, the search page) gained the activity bell (I-6, epic live
// review). Two bars rendering the same dropdown with two hand-copied
// dismiss effects is exactly how one of them ends up not returning focus.

import { useEffect } from "./render.js";

/**
 * useDismissOnOutsideOrEscape closes the open dropdown when a pointer lands
 * outside barRef, and on Escape.
 *
 * Escape also returns focus to the trigger that owns the dropdown, found by
 * its data-picker attribute. Without it Escape closes the menu and leaves
 * the keyboard nowhere: the focused menu item has just been removed from
 * the document, so the next Tab restarts from the top of the page. The
 * trigger is queried fresh rather than captured at open, because it is
 * re-rendered while the menu is up (its own aria-expanded changes).
 */
export function useDismissOnOutsideOrEscape(barRef, openPicker, close) {
  useEffect(() => {
    if (openPicker === null) return;
    function handlePointerDown(e) {
      if (barRef.current && !barRef.current.contains(e.target)) close();
    }
    function handleKeyDown(e) {
      if (e.key !== "Escape") return;
      const trigger = barRef.current?.querySelector(
        `[data-picker="${openPicker}"]`,
      );
      close();
      if (trigger instanceof HTMLElement) trigger.focus();
    }
    document.addEventListener("pointerdown", handlePointerDown);
    document.addEventListener("keydown", handleKeyDown);
    return () => {
      document.removeEventListener("pointerdown", handlePointerDown);
      document.removeEventListener("keydown", handleKeyDown);
    };
  }, [barRef, openPicker, close]);
}
