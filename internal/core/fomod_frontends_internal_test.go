package core

// #615: what the FOMOD frontends need from the resolve step - a later
// step's preselection after a partial answer, a reconfigure's question
// over choices that still fit, and the option images a question keeps.

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/fomod"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// twoStepModuleConfig: step one picks a texture size and sets a flag; step
// two is visible only for 4K and recommends one of two options; each
// option has an image.
const twoStepModuleConfig = `<?xml version="1.0"?>
<config>
  <moduleName>Two Steps</moduleName>
  <installSteps order="Explicit">
    <installStep name="Size">
      <optionalFileGroups order="Explicit">
        <group name="Texture" type="SelectExactlyOne">
          <plugins order="Explicit">
            <plugin name="2K">
              <description>Light.</description>
              <image path="fomod\images\2K.png"/>
              <files><file source="tex\2k.dds" destination="textures\t.dds"/></files>
              <typeDescriptor><type name="Optional"/></typeDescriptor>
            </plugin>
            <plugin name="4K">
              <image path="fomod/images/evil.svg"/>
              <files><file source="tex\4k.dds" destination="textures\t.dds"/></files>
              <conditionFlags><flag name="big">on</flag></conditionFlags>
              <typeDescriptor><type name="Optional"/></typeDescriptor>
            </plugin>
          </plugins>
        </group>
      </optionalFileGroups>
    </installStep>
    <installStep name="Extras">
      <visible><flagDependency flag="big" value="on"/></visible>
      <optionalFileGroups order="Explicit">
        <group name="Pick" type="SelectAny">
          <plugins order="Explicit">
            <plugin name="Readme">
              <image path="..\..\etc\passwd"/>
              <files><file source="docs\readme.txt" destination="readme.txt"/></files>
              <typeDescriptor><type name="Recommended"/></typeDescriptor>
            </plugin>
            <plugin name="Other">
              <files><file source="docs\readme.txt" destination="other.txt"/></files>
              <typeDescriptor><type name="Optional"/></typeDescriptor>
            </plugin>
          </plugins>
        </group>
      </optionalFileGroups>
    </installStep>
  </installSteps>
</config>`

// pngBytes is a minimal PNG header, enough for content sniffing.
var pngBytes = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x02\x00\x00\x00")

func twoStepTree(t *testing.T) string {
	return fomodTree(t, "", twoStepModuleConfig, map[string]string{
		"fomod/images/2k.png":   string(pngBytes),
		"fomod/images/evil.svg": `<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`,
	})
}

func sizeAnswer(size string) domain.FomodChoices {
	return domain.FomodChoices{Steps: []fomod.StepChoice{{Name: "Size", Groups: []fomod.GroupChoice{
		{Name: "Texture", Choices: []fomod.OptionChoice{{Name: size}}},
	}}}}
}

// TestResolveFomod_PartialAnswerKeepsTheDialogPreselection: an answer that
// covers only the first step is asked again about the step it made
// visible, preselected as the installer's dialog would be (Recommended on),
// not as a headless replay fills a group it was never told about.
func TestResolveFomod_PartialAnswerKeepsTheDialogPreselection(t *testing.T) {
	_, err := (&Service{}).resolveFomod(fomodTestMod, "mod.zip", twoStepTree(t), fomodInput{choices: sizeAnswer("4K"), answered: true})
	var q *FomodChoicesRequiredError
	require.True(t, errors.As(err, &q), "got %v", err)
	question := q.Mods[0]
	assert.Equal(t, FomodReasonUnanswered, question.Reason)
	require.Len(t, question.Steps, 2, "the 4K answer made the second step visible")
	assert.True(t, question.Steps[0].Groups[0].Answered)
	extras := question.Steps[1].Groups[0]
	assert.False(t, extras.Answered)
	assert.True(t, extras.Options[0].Selected, "Recommended is preselected")
	assert.False(t, extras.Options[1].Selected)
	assert.Equal(t, []FomodGroupRef{{Step: "Extras", Group: "Pick"}}, question.Unanswered)
}

// TestResolveFomod_AskReconfiguresChoicesThatFit: a reconfigure asks even
// though the recorded choices fit, with them as the preselection; its
// answer installs.
func TestResolveFomod_AskReconfiguresChoicesThatFit(t *testing.T) {
	recorded, err := (&Service{}).resolveFomod(fomodTestMod, "mod.zip", fomodTree(t, "", testModuleConfig, nil), fomodInput{choices: pick("4K"), answered: true})
	require.NoError(t, err)

	_, err = (&Service{}).resolveFomod(fomodTestMod, "mod.zip", fomodTree(t, "", testModuleConfig, nil), fomodInput{choices: recorded.choices, ask: true})
	var q *FomodChoicesRequiredError
	require.True(t, errors.As(err, &q), "got %v", err)
	question := q.Mods[0]
	assert.Equal(t, FomodReasonReconfigure, question.Reason)
	assert.Empty(t, question.Unanswered)
	assert.Equal(t, recorded.choices.Steps, question.Choices, "the recorded choices are the preselection")
	assert.True(t, question.Steps[0].Groups[0].Options[1].Selected, "4K")

	got, err := (&Service{}).resolveFomod(fomodTestMod, "mod.zip", fomodTree(t, "", testModuleConfig, nil), fomodInput{choices: pick("2K"), answered: true, ask: true})
	require.NoError(t, err, "an answer is never asked again")
	assert.NotEqual(t, recorded.variant, got.variant)
}

// TestFomodImage_KeepsOnlyContainedImages: a question keeps the option
// images that are archive members of an image type, under names no XML path
// reaches; FomodImage serves them and nothing else.
func TestFomodImage_KeepsOnlyContainedImages(t *testing.T) {
	data := t.TempDir()
	svc := &Service{dataDir: data}
	_, err := svc.resolveFomod(fomodTestMod, "mod.zip", twoStepTree(t), fomodInput{choices: sizeAnswer("4K"), answered: true})
	var q *FomodChoicesRequiredError
	require.True(t, errors.As(err, &q), "got %v", err)
	question := q.Mods[0]

	img, err := svc.FomodImage("src", "m1", question.ConfigSHA256, `fomod\images\2K.png`)
	require.NoError(t, err)
	assert.Equal(t, "image/png", img.ContentType)
	assert.Equal(t, int64(len(pngBytes)), img.Size)
	assert.True(t, filepath.IsLocal(mustRel(t, filepath.Join(data, stagingDirName, fomodImagesDirName), img.Path)), "kept under the staging root")
	body, err := os.ReadFile(img.Path)
	require.NoError(t, err)
	assert.Equal(t, pngBytes, body)

	for name, path := range map[string]string{
		"svg is not served":        "fomod/images/evil.svg",
		"escape is never resolved": `..\..\etc\passwd`,
		"absolute":                 "/etc/passwd",
		"not named by a question":  "core/plugin.esp",
		"empty":                    "",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := svc.FomodImage("src", "m1", question.ConfigSHA256, path)
			assert.ErrorIs(t, err, ErrFomodImageNotFound)
		})
	}
	_, err = svc.FomodImage("src", "other", question.ConfigSHA256, "fomod/images/2k.png")
	assert.ErrorIs(t, err, ErrFomodImageNotFound, "another mod's question keeps its own images")
	_, err = svc.FomodImage("src", "m1", "../../x", "fomod/images/2k.png")
	assert.ErrorIs(t, err, ErrFomodImageNotFound, "a config hash is a hash")
}

func TestFomodImage_RefusesOversizedImages(t *testing.T) {
	data := t.TempDir()
	svc := &Service{dataDir: data}
	big := append(append([]byte{}, pngBytes...), make([]byte, maxFomodImageSize)...)
	tree := fomodTree(t, "", twoStepModuleConfig, map[string]string{"fomod/images/2k.png": string(big)})
	_, err := svc.resolveFomod(fomodTestMod, "mod.zip", tree, fomodInput{})
	var q *FomodChoicesRequiredError
	require.True(t, errors.As(err, &q), "got %v", err)
	_, err = svc.FomodImage("src", "m1", q.Mods[0].ConfigSHA256, "fomod/images/2k.png")
	assert.ErrorIs(t, err, ErrFomodImageNotFound)
}

func mustRel(t *testing.T, base, path string) string {
	t.Helper()
	rel, err := filepath.Rel(base, path)
	require.NoError(t, err)
	return rel
}
