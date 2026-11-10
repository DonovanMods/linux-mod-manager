# Vendored fonts - DO NOT EDIT

The web UI's two typefaces (#523), embedded in the binary by `go:embed`
(`spa.go`) and served from `/vendor/` like the vendored JavaScript, so every
machine renders the same face and nothing is fetched from the network.
`app.css` declares them with `@font-face` and keeps the system faces as
fallbacks in `--font-sans`/`--font-mono`.

A `.woff2` file cannot carry a header comment, so this file is where each one's
provenance is recorded, the way the `.js` files record theirs in their own
headers. `internal/notices`' `TestVendoredTableMatchesFileHeaders` holds every
`.woff2` in this directory to an entry here whose SHA-256 matches its bytes.

Each file is the upstream artifact **verbatim**: no subsetting, no format
conversion, no renaming inside the font. Neither license names a Reserved Font
Name (each `OFL.txt` copyright line is a bare copyright, with no "with Reserved
Font Name ..." clause), but shipping the files unmodified keeps the question
from arising: they are not Modified Versions under OFL-1.1.

To re-verify: download the release archive, extract the member named below and
compare its sha256 against the value recorded here. Nothing in this repo
fetches them.

## Inter

- Package: Inter
- Version: 4.1 (pinned)
- Source: <https://github.com/rsms/inter/releases/download/v4.1/Inter-4.1.zip>
  (archive SHA-256
  `9883fdd4a49d4fb66bd8177ba6625ef9a64aa45899767dde3d36aa425756b11e`)
- License: OFL-1.1, Copyright (c) 2016 The Inter Project Authors. Full text:
  `LICENSE.inter` (the archive's `LICENSE.txt`, this directory) and, with
  every third-party notice, `THIRD_PARTY_NOTICES` at the repo root.

| File                         | Archive member                   | SHA-256                                                            |
| ---------------------------- | -------------------------------- | ------------------------------------------------------------------ |
| `InterVariable.woff2`        | `web/InterVariable.woff2`        | `693b77d4f32ee9b8bfc995589b5fad5e99adf2832738661f5402f9978429a8e3` |
| `InterVariable-Italic.woff2` | `web/InterVariable-Italic.woff2` | `e564f652916db6c139570fefb9524a77c4d48f30c92928de9db19b6b5c7a262a` |

Both are the variable-weight (100-900) faces. The italic is shipped because
the UI does set text in italic: `.richtext__note`, and the `<em>`/`<i>` a mod
description's markup renders.

## JetBrains Mono

- Package: JetBrains Mono
- Version: 2.304 (pinned)
- Source:
  <https://github.com/JetBrains/JetBrainsMono/releases/download/v2.304/JetBrainsMono-2.304.zip>
  (archive SHA-256
  `6f6376c6ed2960ea8a963cd7387ec9d76e3f629125bc33d1fdcd7eb7012f7bbf`)
- License: OFL-1.1, Copyright 2020 The JetBrains Mono Project Authors. Full
  text: `LICENSE.jetbrains-mono` (the archive's `OFL.txt`, this directory) and,
  with every third-party notice, `THIRD_PARTY_NOTICES` at the repo root.

| File                          | Archive member                               | SHA-256                                                            |
| ----------------------------- | -------------------------------------------- | ------------------------------------------------------------------ |
| `JetBrainsMono-Regular.woff2` | `fonts/webfonts/JetBrainsMono-Regular.woff2` | `a9cb1cd82332b23a47e3a1239d25d13c86d16c4220695e34b243effa999f45f2` |
| `JetBrainsMono-Bold.woff2`    | `fonts/webfonts/JetBrainsMono-Bold.woff2`    | `c503cc5ec5f8b2c7666b7ecda1adf44bd45f2e6579b2eba0fc292150416588a2` |

These are two STATIC weights, not a variable face: the release ships its
variable font only as `.ttf` (`fonts/variable/JetBrainsMono[wght].ttf`), and
converting that to `.woff2` would be a modification. Monospace text in the UI
(paths, ids, code in mod descriptions) is set at the regular weight, and at
bold only where it sits inside bold text, so the two cover it; there is no
mono italic, and the browser slants the regular face for the rare `<em><code>`.
