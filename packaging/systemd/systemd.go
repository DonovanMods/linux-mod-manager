// Package systemd embeds the systemd unit files lmm ships (#567), so the
// file a package installs and the file `lmm service install` writes come
// from the same bytes: internal/core renders its copy from ServeUnit, and
// .goreleaser.yaml installs lmm-serve.service from this directory as it
// stands.
package systemd

import _ "embed"

// ServeUnitName is the unit's file name, the same everywhere it is
// installed.
const ServeUnitName = "lmm-serve.service"

// ServeUnit is lmm-serve.service as packages install it: ExecStart names
// /usr/bin/lmm.
//
//go:embed lmm-serve.service
var ServeUnit []byte
