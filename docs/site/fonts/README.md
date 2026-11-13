# Fonts

The landing page's two typefaces are the web UI's, copied byte for byte from
`internal/serve/vendor/` (#625): Inter 4.1 (`InterVariable.woff2`) and
JetBrains Mono 2.304 (`JetBrainsMono-Regular.woff2`), both under the SIL Open
Font License 1.1. Their license texts sit beside them (`LICENSE.inter`,
`LICENSE.jetbrains-mono`), and their provenance and SHA-256 sums are recorded
in `internal/serve/vendor/FONTS.md`.

`cmd/lmm/site_test.go`'s `TestSiteFontsAreTheVendoredFonts` fails if a copy
here differs from its vendored original, so upgrade a font there first and
copy it here again.
