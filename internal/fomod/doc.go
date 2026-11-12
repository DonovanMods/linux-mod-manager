// Package fomod reads FOMOD installers (fomod/ModuleConfig.xml) and
// resolves a set of installer choices into install operations, with
// Vortex's semantics (#354, #604; design: docs/plans/2026-11-10-collections-design.md §4).
//
// It is pure: standard library only, no filesystem, no lmm imports. Core
// supplies the archive's member names, the recorded choices and a
// FileStates for fileDependency, and executes the returned Ops itself.
// Nothing an archive contains is ever executed: a C# scripted installer
// (fomod/script.cs) or an OMOD is refused with ErrScripted.
//
// # Flow
//
// Locate finds the installer in an archive's member list. Parse reads its
// ModuleConfig.xml. Evaluate walks the steps with a []StepChoice (Vortex's
// collection.json choice format, which decodes into it directly) and
// returns the Ops to perform, the visible steps with each option's state
// (the raw material of a question to the user), the canonical choices to
// record, and what did not fit: unanswered groups, unmatched choices, rule
// violations, missing sources, and the conditions it assumed.
//
// # Semantics
//
// Collections are authored in Vortex, so a replay must reproduce Vortex's
// result. The rules Vortex's installer applies, and this package mirrors:
//
//   - Steps are walked in order; a step whose <visible> condition fails
//     under the flags set so far is skipped. Selecting an option sets its
//     flags; deselecting it clears the flags it still owns (a later option
//     setting the same flag takes it over). Flag names and values compare
//     case-sensitively; an empty expected value matches an unset flag.
//   - A missing `order` attribute means Ascending (the XSD's default, which
//     Vortex's schema validation applies), so steps, groups and plugins sort
//     by name unless order="Explicit". The sort approximates .NET's
//     culture-aware compare: case-insensitive, then lower-case first.
//   - Option types: the first dependencyType pattern whose condition holds
//     wins, else the default. Required options are forced on and NotUsable
//     ones off. A group with more than one Required option is relaxed
//     (SelectExactlyOne to SelectAtLeastOne, SelectAtMostOne to SelectAny).
//   - Recorded choices match by name only, and a name selects every
//     same-named sibling. Within an answered group, unpicked options are
//     off (Required ones come back on). A recorded NotUsable option becomes
//     CouldBeUsable and stays picked. An unanswered SelectExactlyOne group
//     takes its first option. ModeDefaults additionally preselects
//     Recommended options, as Vortex's dialog does.
//   - Install layering: required files at their priority - 1e9, files of
//     selected options at their priority, conditional installs at their
//     priority + 1e9. Unselected options still install files marked
//     alwaysInstall, or installIfUsable when the option is not NotUsable,
//     in every step, visible or not. The higher priority wins a
//     destination; a tie goes to the lexicographically larger source path.
//   - An absent `destination` means the source path; an empty one means the
//     source's base name at the root (a folder's contents at the root). A
//     <file> destination ending in a separator only creates that directory,
//     and the file is not installed (reported as a warning).
//   - Paths are backslash-separated in the XML and matched against archive
//     members case-insensitively after normalising both.
//   - Encoding is sniffed from the bytes (UTF-8 with or without a BOM,
//     UTF-16 LE/BE with or without a BOM, Windows-1252) and the XML
//     declaration is ignored: it is usually wrong.
//
// # Where lmm departs from Vortex
//
//   - <folder> entries are layered too. Vortex installs a <folder> in
//     requiredInstallFiles or conditionalFileInstalls at its raw priority,
//     skipping the ±1e9 offset it applies to <file> entries. That reads as
//     a bug, and matching it would cost a quirk forever for an edge case no
//     one depends on knowingly.
//   - Recorded choices that select nothing are reported (Result.Unmatched),
//     where Vortex skips them silently: a stale choice means the installer
//     changed under the recording. A recorded entry may also carry an
//     Index, lmm's own ordinal among same-named siblings in document order,
//     to pick one of several same-named items; Vortex's own idx is carried
//     but, as in Vortex, never compared.
//   - Group rules are checked after the walk (Result.Violations); Vortex's
//     headless replay does not check them.
//   - Without a FileStates every file is Missing. Vortex, with no host to
//     ask, fails every fileDependency, Missing included.
//   - gameDependency, fommDependency and the script-extender dependencies
//     (skseDependency, ...) are treated as satisfied with a warning: lmm
//     cannot read a Windows executable's version. Unknown dependency
//     elements are treated the same way.
//   - Parsing is tolerant where Vortex validates strictly against the XSD:
//     an unrecognised value falls back to a default with a Config.Warnings
//     entry rather than failing the install.
//   - Destinations collide case-insensitively (Vortex keys them exactly,
//     then writes to a case-insensitive filesystem, so the later copy wins
//     regardless of priority). A copy to "a" cannot coexist with a
//     directory at "a" or anything under "a/": the stronger side by the
//     same priority-then-source rule is kept, and the other dropped with a
//     warning.
//   - Composite conditions short-circuit; Vortex evaluates every child.
//     Only which assumed dependencies get a warning can differ.
//   - Two installers at the same level of an archive, even ones differing
//     only in case, are ErrAmbiguous.
//
// # Untrusted input
//
// The ModuleConfig.xml and the archive's member names are both
// author-controlled. A destination from the XML that leaves the mod root
// (".."), contains a NUL, or is a drive-letter or UNC path fails the
// evaluation with ErrUnsafePath. A member name with a ".." segment, an
// absolute or drive-letter path, or a NUL is dropped before matching and
// listed in Result.UnsafeMembers, so one hostile member cannot fail the
// whole install; the caller decides whether it blocks. Op.Source is
// always a member exactly as the caller spelled it. Every dimension is
// capped: MaxConfigSize bytes, MaxDepth nesting and MaxNodes elements per
// document, MaxChoices recorded entries, and MaxOps operations and
// MaxConditionEvals condition visits per evaluation (ErrTooLarge or
// ErrTooDeep beyond them). Type resolution, flag bookkeeping and choice
// matching are indexed, so evaluation is linear in the document.
package fomod
