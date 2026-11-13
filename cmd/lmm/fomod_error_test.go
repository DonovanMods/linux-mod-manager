package main

// #614: a FOMOD install's two refusals render their whole document under
// --json's "details": the question a frontend answers, and the refusal no
// answer fixes.

import (
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
)

func TestReportError_JSON_FomodChoicesRequiredError(t *testing.T) {
	withJSONOutput(t)

	err := &core.FomodChoicesRequiredError{Mods: []core.FomodQuestion{{
		SourceID: "nexusmods", ModID: "12604", Name: "Sample Mod", ConfigSHA256: "ab12",
		ModuleName: "Sample Installer", Reason: core.FomodReasonUnanswered,
		Steps: []core.FomodQuestionStep{{Name: "Main", Groups: []core.FomodQuestionGroup{{
			Name: "Texture", Type: "SelectExactlyOne",
			Options: []core.FomodQuestionOption{{Name: "2K", Type: "Optional", Selected: true}},
		}}}},
		Choices:    []domain.FomodStepChoice{{Name: "Main", Groups: []domain.FomodGroupChoice{{Name: "Texture", Choices: []domain.FomodOptionChoice{{Name: "2K"}}}}}},
		Unanswered: []core.FomodGroupRef{{Step: "Main", Group: "Texture"}},
	}}}
	out := captureStdout(t, func() error { reportError(err); return nil })

	assert.Equal(t, `{
  "error": "Sample Mod has an installer (FOMOD) whose choices need answering; answer them and run the same command again",
  "details": {
    "mods": [
      {
        "source_id": "nexusmods",
        "mod_id": "12604",
        "name": "Sample Mod",
        "config_sha256": "ab12",
        "module_name": "Sample Installer",
        "installer_root": "",
        "reason": "unanswered",
        "steps": [
          {
            "name": "Main",
            "ordinal": 0,
            "groups": [
              {
                "name": "Texture",
                "ordinal": 0,
                "type": "SelectExactlyOne",
                "answered": false,
                "options": [
                  {
                    "name": "2K",
                    "ordinal": 0,
                    "type": "Optional",
                    "selected": true
                  }
                ]
              }
            ]
          }
        ],
        "choices": [
          {
            "name": "Main",
            "groups": [
              {
                "name": "Texture",
                "choices": [
                  {
                    "name": "2K",
                    "idx": 0
                  }
                ]
              }
            ]
          }
        ],
        "unanswered": [
          {
            "step": "Main",
            "group": "Texture"
          }
        ]
      }
    ]
  }
}
`, out)
}

func TestReportError_JSON_FomodInstallError(t *testing.T) {
	withJSONOutput(t)

	err := &core.FomodInstallError{SourceID: "nexusmods", ModID: "12604", Name: "Sample Mod",
		Reason: "it is a scripted installer (fomod/script.cs or an OMOD), which lmm never runs"}
	out := captureStdout(t, func() error { reportError(err); return nil })

	assert.Equal(t, `{
  "error": "cannot install Sample Mod: it is a scripted installer (fomod/script.cs or an OMOD), which lmm never runs",
  "details": {
    "source_id": "nexusmods",
    "mod_id": "12604",
    "name": "Sample Mod",
    "reason": "it is a scripted installer (fomod/script.cs or an OMOD), which lmm never runs"
  }
}
`, out)
}
