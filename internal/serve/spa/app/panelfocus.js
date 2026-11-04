// panelfocus.js - a one-shot "focus this control when the slide-over opens"
// request (issue 592).
//
// The library row menu's "Set category…" opens the slide-over the way a
// row click does - by navigating to ?mod= - and the slide-over then focuses
// itself on open. A route carries no "and then focus the category field",
// and putting one in the URL would make a reload or a shared link re-focus a
// field nobody asked for, so the request is handed over here instead: the
// menu files it just before navigating, and the panel takes it (once) in
// its open effect.

//
// The request names the mod it is for, so one left untaken (the panel was
// already open on that mod, say) never focuses the field of another mod's
// panel opened later.

let pending = null;

/** Asks the slide-over for modKey (its ?mod= "source/id") to open with
 * `control` focused ("category"). */
export function requestPanelFocus(modKey, control) {
  pending = { modKey, control };
}

/** Clears the pending request and returns its control when it was for
 * modKey, else null. */
export function takePanelFocus(modKey) {
  const request = pending;
  pending = null;
  return request?.modKey === modKey ? request.control : null;
}
