package main

import (
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"

	"github.com/stretchr/testify/assert"
)

// TestReportError_JSON_UpdateTargetUnavailableError pins the wire shape of
// #505's refusal: an update whose advertised file is gone upstream. It is the
// detailsCoverage entry for core.UpdateTargetUnavailableError, and the same
// document a failed `lmm serve` update job carries in its error envelope, so
// a script gets the candidate files as data and can pass one to
// `lmm install --file` without parsing the sentence.
func TestReportError_JSON_UpdateTargetUnavailableError(t *testing.T) {
	withJSONOutput(t)

	err := &core.UpdateTargetUnavailableError{
		SourceID: "curseforge", ModID: "4242", ModName: "Big Pack", Profile: "default",
		TargetVersion:  "1.9.0",
		MissingFileIDs: []string{"8999930"},
		Advertised:     true,
		Candidates: []core.UpdateTargetCandidate{
			{ID: "8999990", Name: "bigpack-fabric-1.20.1-1.9.0", Version: "1.9.0", Category: "release"},
		},
	}
	out := captureStdout(t, func() error { reportError(err); return nil })

	assert.Equal(t, "{\n"+
		"  \"error\": \"cannot update Big Pack to 1.9.0: the file the update check named (file ID 8999930) is no longer offered by the source, and lmm will not guess a replacement from the version label. Re-run 'lmm update' so the check names a file that exists, or install the file you want explicitly with 'lmm install --source curseforge --id 4242 --profile default --file <file-id>'; files listed under 1.9.0: 8999990 \\\"bigpack-fabric-1.20.1-1.9.0\\\"\",\n"+
		"  \"details\": {\n"+
		"    \"source_id\": \"curseforge\",\n"+
		"    \"mod_id\": \"4242\",\n"+
		"    \"mod_name\": \"Big Pack\",\n"+
		"    \"profile\": \"default\",\n"+
		"    \"target_version\": \"1.9.0\",\n"+
		"    \"missing_file_ids\": [\n"+
		"      \"8999930\"\n"+
		"    ],\n"+
		"    \"advertised\": true,\n"+
		"    \"candidates\": [\n"+
		"      {\n"+
		"        \"id\": \"8999990\",\n"+
		"        \"name\": \"bigpack-fabric-1.20.1-1.9.0\",\n"+
		"        \"version\": \"1.9.0\",\n"+
		"        \"category\": \"release\"\n"+
		"      }\n"+
		"    ]\n"+
		"  }\n"+
		"}\n", out)
}
