package core

import (
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/fomod"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type reviewStates map[string]fomod.FileState

func (r reviewStates) State(p string) fomod.FileState {
	if s, ok := r[strings.ToLower(p)]; ok {
		return s
	}
	return fomod.FileMissing
}

func TestReview_FileStatesChangeTreeNotVariant(t *testing.T) {
	cfg := strings.Replace(testModuleConfig, "</installSteps>", `</installSteps>
<conditionalFileInstalls><patterns><pattern><dependencies><fileDependency file="dep.esp" state="Active"/></dependencies>
<files><file source="docs\readme.txt" destination="patch.txt"/></files></pattern></patterns></conditionalFileInstalls>`, 1)
	t1 := fomodTree(t, "", cfg, nil)
	a, err := (&Service{}).resolveFomod(fomodTestMod, "mod.zip", t1, fomodInput{choices: pick("2K"), answered: true})
	require.NoError(t, err)
	t2 := fomodTree(t, "", cfg, nil)
	b, err := (&Service{}).resolveFomod(fomodTestMod, "mod.zip", t2, fomodInput{choices: pick("2K"), answered: true, files: reviewStates{"dep.esp": fomod.FileActive}})
	require.NoError(t, err)
	t.Logf("tree1=%v tree2=%v", fomodTreeFiles(t, t1), fomodTreeFiles(t, t2))
	assert.NotEqual(t, a.variant, b.variant)
}
