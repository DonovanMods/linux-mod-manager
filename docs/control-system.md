# Control system

Every control and every form in the web UI (`lmm serve`) is drawn by one
stylesheet: the control block in `internal/serve/spa/app.css`, between
`/* @control-system begin */` and `/* @control-system end */`. There is no
CSS framework. New UI uses the classes below and builds no layout of its
own. The design history is in
[docs/plans/2026-08-31-serve-spa-design.md](plans/2026-08-31-serve-spa-design.md)
(§Controls, #518 and #653).

## Which class for what

### Buttons

| You need | Use |
| --- | --- |
| An ordinary action | `button` on a `<button>` (or an `<a>` that acts like one) |
| The one main action of a bar, card footer, modal footer or form | `button button--primary` |
| Something that deletes or removes | `button button--danger` |
| A picker or menu trigger (draws its ▾), or a glyph-only control (✕, ?, ‹ ›) | `button button--quiet`, with `picker__trigger` or `button--icon` |
| Text that acts (a row's name, an inline "View") | `button button--link` |
| A tab in a `role="tablist"` | `button button--tab` |
| A whole card that is one button | `button button--tile` |
| A button labelled with a title of any length | `button button--prose` |
| An item in a dropdown menu | `menu-item` |
| A dense context (table rows, a card's item rows, menus) | add `button--small`; never beside default-size controls |

### Fields

| You need | Use |
| --- | --- |
| A one-line text field | `<input type="text">` (or `search`, `url`, `number`, `email`, `tel`, `password`): always name the type |
| A choice from a list | `<select>` |
| Several lines of text | `<textarea>` |
| A path, an id or source text | add `field--mono` |
| A code editor's textarea inside its own frame | add `field--code` |
| A checkbox or radio | `<input type="checkbox">` / `type="radio"` inside a `.form-check` label |
| An on/off setting that takes effect at once | `<input type="checkbox" role="switch">` |
| A file to upload | `FilePicker` from `components/filepicker.js`: a `.button` over a hidden file input. Never a `<label>` dressed as a button |
| A field the form refuses | `aria-invalid="true"` on the field, and the reason in a `.form-error` |

A one-line field is exactly `--control-height`, the default button's height,
so a field and its button line up. Disabled controls use `disabled`; the
block draws them.

### Form layout

| You need | Use |
| --- | --- |
| A titled unit: heading, fields, actions, hint | `.form-section` (a `<form>`, `<div>` or `<fieldset>`), heading `.form-section__heading` (or a `<legend>`). Any container that holds form parts is one, a `<details>` body included (a `.form-section` inside it) |
| A label over its control | `<label class="form-field">Label <input …></label>` |
| A label over a control that cannot sit inside a `<label>` (a path field with its Browse… button) | `<div class="form-field">` holding `<label class="form-field__label" for=…>` |
| A label beside its control (a toolbar's Filter) | `form-field form-field--inline` |
| A checkbox, radio or switch beside its text | `<label class="form-check"><input type="checkbox"> Text</label>` |
| A field and its button on one line | `.form-row` (`.form-row--top` for fields of unequal height) |
| A form's buttons | `.form-actions` |
| The muted sentence under a field or section | `.form-hint` |
| Why a field was refused | `.form-error` (a server's refusal of the whole request stays `.modal__error`) |
| A section that is a whole page column (Setup) | add `form-section--measure`: fields keep a reading width |

An element class of its own (`profiles-import__url`, `game-editor__field`)
may stay on an element as a hook for tests and for placement (margin,
width, flex), but it never reshapes a control or lays out a form.

## What holds it

- `control_system_test.go`: every `<button>` is `button` or `menu-item`; no
  rule outside the block sets height, padding, border, radius or type size
  on a button or a field (`TestNoControlStylingOutsideTheControlBlock`,
  `TestNoFieldStylingOutsideTheControlBlock`); every `<form>`, `<fieldset>`
  and `<label>` carries a form-layout class
  (`TestEveryFormUsesTheFormLayout`). Exceptions are listed in the file,
  with a reason; the lists are empty.
- `input_types_test.go`: every `<input>` is a type the block draws, and the
  block styles `select` and `textarea`.
- `e2e_controls_test.go`: the top bar and library toolbar share one control
  height.
- `e2e_dialog_controls_test.go`: every modal, the mod slide-over and every
  Setup form (40 scenarios), opened from a table of how to open each, in
  both themes and every layout face: fields and default buttons one height,
  small buttons the small height, a `.form-row`'s field and button on one
  line, and the form rhythm - no part of a form closer to the one before it
  than `--form-gap` (`--form-field-gap` inside a field), so a container that
  is not a `.form-section` cannot collapse its parts together. A second pass
  opens each at 375px: no control overruns its dialog, and a modal keeps a
  16px gutter. A new dialog gets a row; `TestE2E_DialogScenariosCoverEveryKind`
  fails until it has one.
- `contrast_test.go` and `no_hardcoded_color_test.go`: colours come only from
  the theme tokens, at WCAG AA in both themes.
