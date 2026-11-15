// filepicker.js - the one file picker (issue 653).
//
// A native <input type="file"> is never drawn (the control block hides
// it): its look is the browser's, and a <label> dressed as a button around
// one is not focusable at all (issue 652). The picker is a real .button
// that opens a hidden input, so it is sized, focused and labelled like
// every other button here, and this is the one place that pairs the two.

import { html, useRef } from "../render.js";

/**
 * FilePicker renders "Choose…" as a .button over a hidden file input and
 * calls onFiles with the chosen files (an array, never empty). The input is
 * cleared after each choice, so picking the same file again still fires.
 *
 * label is the button's text; inputLabel names the hidden input for
 * assistive technology (TestE2E_EveryFormControlHasAnAccessibleName);
 * variant is "" (secondary), "primary" or "small". testid and inputTestid
 * address the button and the input from a test; action is the button's
 * data-action.
 */
export function FilePicker({
  label,
  inputLabel,
  accept,
  multiple = false,
  disabled = false,
  variant = "",
  testid,
  inputTestid,
  action,
  onFiles,
}) {
  const ref = useRef(null);
  return html`
    <button
      type="button"
      class=${variant ? `button button--${variant}` : "button"}
      data-testid=${testid}
      data-action=${action}
      disabled=${disabled}
      onClick=${() => ref.current?.click()}
    >
      ${label}
    </button>
    <input
      ref=${ref}
      type="file"
      accept=${accept}
      multiple=${multiple}
      aria-label=${inputLabel}
      tabindex="-1"
      aria-hidden="true"
      data-testid=${inputTestid}
      disabled=${disabled}
      hidden
      onChange=${(e) => {
        const files = Array.from(e.currentTarget.files ?? []);
        e.currentTarget.value = "";
        if (files.length > 0) onFiles(files);
      }}
    />
  `;
}
