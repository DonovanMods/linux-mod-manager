# Plugin header fixtures

These plugin files are copied unmodified from
[Ortham/testing-plugins](https://github.com/Ortham/testing-plugins) at commit
`d9b28a2062a12f3a40bc9b13be9d6f5731c81249`, which is MIT-licensed
(Copyright (c) 2014 Oliver Hamlet; the licence is `LICENSE` beside this file).
They are test data for `internal/core/plugins_header.go` (#606) and are not
shipped in any lmm package.

| File here                                         | Upstream path                    | sha256                                                             |
| ------------------------------------------------- | -------------------------------- | ------------------------------------------------------------------ |
| `Fallout4/Blank.esp`                              | `Fallout 4/Data/Blank.esp`       | `60db77a558a1e9fd7bd809bdd7c5a9d32a9baef816f8e7c86c4a8dd6c75e37ea` |
| `Morrowind/Blank.esm`                             | `Morrowind/Data Files/Blank.esm` | `f93788fa2eba9613bf4a9edefcd60cd3c8cc43eee6a45d34b54357fa4b51b2b8` |
| `SkyrimSE/Blank - Different Master Dependent.esm` | `SkyrimSE/Data/…`                | `d48627f200363d7c29f2f2f7b4804d7da6151a43fb840004528a259daf9c4a9d` |
| `SkyrimSE/Blank - Master Dependent.esp`           | `SkyrimSE/Data/…`                | `9607d77703f4af5773e565763aca5ba8db532e806b21a918e85acb0532b8f0df` |
| `SkyrimSE/Blank.esl`                              | `SkyrimSE/Data/Blank.esl`        | `c9debe79facb23a1c030c4c6152eaa9dd1a1a95fc1056a9e1ff6c79b94d982c3` |
| `SkyrimSE/Blank.esm`                              | `SkyrimSE/Data/Blank.esm`        | `44f8ae26e05d5aef505a4115a2dd70bd36749163226ea3d967cad4ce95e04fd7` |
| `SkyrimSE/Blank.esp`                              | `SkyrimSE/Data/Blank.esp`        | `1fdffdbd602e70bbb7c99999f15a739a9dee80847bcf2e7ea506c6a24146c456` |
| `Starfield/Blank - Override.esp`                  | `Starfield/Data/…`               | `d3ce062e37eb70e11024fb6911746dd8ac7b959f9935272501d19f44da00aeef` |
| `Starfield/Blank.full.esm`                        | `Starfield/Data/…`               | `038ed7a1a00b47db120993250f3334aaaefda37fe0d8792d1ed591c9cc13dda4` |
| `Starfield/Blank.medium.esm`                      | `Starfield/Data/…`               | `c080c96770429d2ea8d0e27c1b4fa0594899e9537b9100b98a7eb003bd11688d` |
| `Starfield/Blank.small.esm`                       | `Starfield/Data/…`               | `7b373afc73e8bbd650d434a7b39b4e5a39701aabcadc27638f35da02cda1d32a` |

`SkyrimSE/Blank.esm`'s header carries an `XXXX` subrecord followed by a
16 KiB `ONAM`, which is what exercises the oversize-subrecord path. Edge cases
the corpus lacks (truncation, Windows-1252 names, a compressed header) are
synthesised in the tests.
