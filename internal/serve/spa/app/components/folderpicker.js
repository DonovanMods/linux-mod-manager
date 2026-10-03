// folderpicker.js - the folder chooser (issue 529): a modal that browses the
// SERVER's filesystem through GET /api/v1/fs/dirs, and the one control that
// opens it, FolderField.
//
// It browses the server's disk because a browser cannot name a folder: an
// <input type="file"> hands over a file's contents and a bare name, never a
// location, and a page cannot pick a folder and receive its absolute path.
// `lmm serve` runs on the user's own machine, so the machine's folders are
// the ones to offer - and only folders: core never returns a regular file.
//
// Typing a path by hand stays the primary way to fill a field; this exists
// to cut down on mistyped ones.
//
// Keyboard: the subfolder list is one listbox with a single tab stop and
// aria-activedescendant. Arrow Up/Down, Home and End move through it; Enter
// (or Right) opens the highlighted folder; Backspace (or Left, or Alt+Up)
// goes up. The path bar takes a typed path and Enter. The shell is modal.js,
// so Escape closes, Tab is trapped, and focus returns to Browse….

import { html, render, useEffect, useRef, useState } from "../render.js";
import { listDirectories } from "../api.js";
import { Modal } from "./modal.js";

let pickerSerial = 0;

/**
 * Portal mounts its children as a child of <body> instead of where it sits
 * in the tree. The picker is opened from inside a form, a table row and a
 * 0.85em label, and a modal left in that DOM would inherit their type size
 * and colour, sit under their click handlers, and have its Enter submit
 * their form. The vendored Preact is core only (no createPortal), so this
 * renders into a host element of its own.
 */
function Portal({ children }) {
  const host = useRef(null);
  if (host.current === null) host.current = document.createElement("div");
  useEffect(() => {
    document.body.appendChild(host.current);
    return () => {
      render(null, host.current);
      host.current.remove();
    };
  }, []);
  // After every render of the owner, so the portal's tree follows its props.
  useEffect(() => {
    render(children, host.current);
  });
  return null;
}

/** breadcrumbs splits an absolute path into its clickable segments: the
 * root first, then each folder with the path that leads to it. */
export function breadcrumbs(path) {
  const crumbs = [{ label: "/", path: "/" }];
  let acc = "";
  for (const part of path.split("/")) {
    if (!part) continue;
    acc += `/${part}`;
    crumbs.push({ label: part, path: acc });
  }
  return crumbs;
}

/**
 * FolderPicker is the chooser modal. It opens at `start` (what the field
 * holds) - core answers for that folder's nearest existing ancestor, or home
 * when there is nothing usable - and calls onChoose(absolutePath) from
 * "Choose this folder" or onCancel from every other way out.
 *
 * openerSelector names the Browse… button, so focus returns to it however
 * the modal closes.
 */
export function FolderPicker({
  start,
  title,
  openerSelector,
  onChoose,
  onCancel,
}) {
  const [listing, setListing] = useState(null);
  const [pathText, setPathText] = useState(start ?? "");
  const [hidden, setHidden] = useState(false);
  const [active, setActive] = useState(0);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);
  const listRef = useRef(null);
  const serial = useRef(0);
  const ids = useRef(null);
  if (ids.current === null) ids.current = `folder-picker-${++pickerSerial}`;

  // load asks core for path. A refusal keeps the folder already on screen
  // and says why beside the path bar; a stale answer (the user moved on
  // while it was in flight) is dropped.
  async function load(path, { nearest = false, showHidden = hidden } = {}) {
    const mine = ++serial.current;
    setLoading(true);
    try {
      const next = await listDirectories(path, { hidden: showHidden, nearest });
      if (mine !== serial.current) return;
      setListing(next);
      setPathText(next.path);
      setActive(0);
      setError("");
    } catch (err) {
      if (mine !== serial.current) return;
      setError(err?.message || "That folder could not be read.");
    } finally {
      if (mine === serial.current) setLoading(false);
    }
  }

  useEffect(() => {
    load(start ?? "", { nearest: true });
  }, []);

  // The first listing arrives after the modal has focused its panel; hand
  // the keyboard to the list, so arrows and Enter work at once.
  const focusedOnce = useRef(false);
  useEffect(() => {
    if (listing && !focusedOnce.current) {
      focusedOnce.current = true;
      listRef.current?.focus();
    }
  }, [listing]);

  // Keep the highlighted row on screen.
  useEffect(() => {
    document
      .getElementById(`${ids.current}-opt-${active}`)
      ?.scrollIntoView?.({ block: "nearest" });
  }, [active, listing]);

  const entries = listing?.entries ?? [];

  function open(entry) {
    if (entry) load(entry.path);
  }
  function up() {
    if (listing?.parent) load(listing.parent);
  }

  function onListKeyDown(e) {
    if (e.altKey && e.key === "ArrowUp") {
      e.preventDefault();
      up();
      return;
    }
    switch (e.key) {
      case "ArrowDown":
        e.preventDefault();
        setActive((i) => Math.min(i + 1, entries.length - 1));
        break;
      case "ArrowUp":
        e.preventDefault();
        setActive((i) => Math.max(i - 1, 0));
        break;
      case "Home":
        e.preventDefault();
        setActive(0);
        break;
      case "End":
        e.preventDefault();
        setActive(Math.max(entries.length - 1, 0));
        break;
      case "Enter":
      case "ArrowRight":
        e.preventDefault();
        open(entries[active]);
        break;
      case "Backspace":
      case "ArrowLeft":
        e.preventDefault();
        up();
        break;
    }
  }

  function onPathKeyDown(e) {
    if (e.key !== "Enter") return;
    // The picker can sit inside a <form> (the add-game form): Enter here
    // navigates, it must not also submit that form.
    e.preventDefault();
    e.stopPropagation();
    load(pathText);
  }

  const crumbs = listing ? breadcrumbs(listing.path) : [];
  const activeID = entries.length ? `${ids.current}-opt-${active}` : undefined;

  return html`
    <${Modal}
      kind="folder-picker"
      title=${title}
      onClose=${onCancel}
      openerSelector=${openerSelector}
      footer=${html`
        <button
          type="button"
          class="button"
          data-action="folder-cancel"
          onClick=${onCancel}
        >
          Cancel
        </button>
        <button
          type="button"
          class="button button--primary"
          data-action="folder-choose"
          disabled=${!listing}
          onClick=${() => listing && onChoose(listing.path)}
        >
          Choose this folder
        </button>
      `}
    >
      <div class="folder-picker" data-testid="folder-picker">
        <div class="folder-picker__bar">
          <button
            type="button"
            class="button"
            data-action="folder-up"
            disabled=${!listing?.parent}
            onClick=${up}
          >
            Up
          </button>
          <input
            type="text"
            class="mono"
            name="folder-path"
            aria-label="Folder path"
            spellcheck=${false}
            autocomplete="off"
            value=${pathText}
            onInput=${(e) => setPathText(e.currentTarget.value)}
            onKeyDown=${onPathKeyDown}
          />
          <button
            type="button"
            class="button"
            data-action="folder-go"
            onClick=${() => load(pathText)}
          >
            Go
          </button>
        </div>

        ${
          listing &&
          html`<nav class="folder-picker__crumbs" aria-label="Path">
            ${crumbs.map(
              (c, i) => html`
                <button
                  type="button"
                  key=${c.path}
                  class="button button--small"
                  data-crumb=${c.path}
                  aria-current=${i === crumbs.length - 1 ? "location" : undefined}
                  onClick=${() => load(c.path)}
                >
                  ${c.label}
                </button>
              `,
            )}
          </nav>`
        }

        <div class="folder-picker__answer" role="status">
          ${
            error &&
            html`<p class="modal__error" data-testid="folder-picker-error">
              ${error}
            </p>`
          }
          ${
            !error &&
            listing?.requested &&
            html`<p
              class="empty-state__hint"
              data-testid="folder-picker-nearest"
            >
              ${listing.requested} does not exist; showing the nearest folder
              that does.
            </p>`
          }
        </div>

        <label class="plan__control plan__control--inline">
          <input
            type="checkbox"
            name="folder-hidden"
            checked=${hidden}
            onChange=${(e) => {
              const on = e.currentTarget.checked;
              setHidden(on);
              if (listing) load(listing.path, { showHidden: on });
            }}
          />
          Show hidden folders
        </label>

        <ul
          class="folder-picker__list"
          role="listbox"
          tabindex="0"
          ref=${listRef}
          aria-label=${listing ? `Folders in ${listing.path}` : "Folders"}
          aria-busy=${loading ? "true" : "false"}
          aria-activedescendant=${activeID}
          data-testid="folder-picker-list"
          onKeyDown=${onListKeyDown}
        >
          ${
            entries.length === 0 &&
            html`<li class="folder-picker__empty" role="presentation">
              ${loading ? "Reading…" : "No subfolders"}
            </li>`
          }
          ${entries.map(
            (entry, i) => html`
              <li
                key=${entry.path}
                id=${`${ids.current}-opt-${i}`}
                role="option"
                aria-selected=${i === active ? "true" : "false"}
                class=${`folder-picker__entry mono${entry.readable ? "" : " folder-picker__entry--locked"}`}
                data-folder=${entry.name}
                onClick=${() => {
                  setActive(i);
                  listRef.current?.focus();
                }}
                onDblClick=${() => open(entry)}
              >
                <span class="folder-picker__name">${entry.name}</span>
                ${entry.symlink && html`<span class="folder-picker__tag">link</span>`}
                ${entry.hidden && html`<span class="folder-picker__tag">hidden</span>`}
                ${!entry.readable && html`<span class="folder-picker__tag">no access</span>`}
              </li>
            `,
          )}
        </ul>
      </div>
    <//>
  `;
}

let fieldSerial = 0;

/** isAbsolutePathText reports whether text can name a starting folder by
 * itself: absolute, or ~ (core expands it). Anything else - empty, or
 * relative - cannot. */
export function isAbsolutePathText(text) {
  const t = (text ?? "").trim();
  return t.startsWith("/") || t === "~" || t.startsWith("~/");
}

/**
 * BrowseButton is a button that opens the chooser and reports the folder
 * chosen. FolderField puts one beside a directory input; the custom-source
 * editor, whose path lives inside a YAML textarea, puts one under it.
 *
 *   start      where the chooser opens: a path, or a function returning one
 *              that is called when the button is pressed
 *   onOpen     called when the button is pressed, before the chooser opens
 *              (a textarea records its selection here)
 *   label      what is being chosen, for the chooser's title
 *   text       the button's visible label (default "Browse…")
 *   ariaLabel  its accessible name when that is longer than its text
 *   action     its data-action
 *   onChoose   called with the chosen absolute path, after the chooser has
 *              closed and focus has returned to this button
 */
export function BrowseButton({
  start,
  onOpen,
  label,
  text = "Browse…",
  ariaLabel,
  action = "browse-folder",
  disabled,
  onChoose,
}) {
  const [open, setOpen] = useState(null); // null, or where it opened
  const id = useRef(null);
  if (id.current === null) id.current = `folder-browse-${++fieldSerial}`;

  return html`
    <button
      type="button"
      id=${id.current}
      class="button"
      data-action=${action}
      aria-label=${ariaLabel}
      disabled=${disabled}
      onClick=${() => {
        onOpen?.();
        setOpen({ at: typeof start === "function" ? start() : (start ?? "") });
      }}
    >
      ${text}
    </button>
    ${
      open &&
      html`<${Portal}>
        <${FolderPicker}
          start=${open.at}
          title=${`Choose ${label}`}
          openerSelector=${`#${id.current}`}
          onCancel=${() => setOpen(null)}
          onChoose=${(path) => {
            setOpen(null);
            onChoose(path);
          }}
        />
      <//>`
    }
  `;
}

/**
 * FolderField is a directory input with its "Browse…" button: the input the
 * caller already had, and the chooser behind the button. Typing still works.
 *
 * `children` is the <input> itself, supplied by the caller so it keeps its
 * own id, name, ARIA wiring and handlers; this component only lays the row
 * out and hands a chosen path to onChoose.
 *
 *   value        the field's current text (the picker opens there)
 *   fallback     where to open when value is empty or relative - a mod path
 *                is relative to the install path, so its fallback is that
 *   label        the field's name, for the picker's title and the button's
 *                accessible name ("Browse… install path")
 *   onChoose     called with the chosen absolute path
 */
export function FolderField({
  value,
  fallback,
  label,
  disabled,
  onChoose,
  children,
}) {
  const text = (value ?? "").trim();
  return html`
    <div class="folder-field">
      ${children}
      <${BrowseButton}
        start=${isAbsolutePathText(text) ? text : (fallback ?? "")}
        label=${label}
        ariaLabel=${`Browse… ${label}`}
        disabled=${disabled}
        onChoose=${onChoose}
      />
    </div>
  `;
}
