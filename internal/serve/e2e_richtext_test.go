package serve_test

// #419: a mod description's BBCode or Markdown renders as a SAFE element
// tree. spa/app/richtext.js is plain JavaScript with no runtime outside a
// browser (there is no Node here, by design), so its table tests run the
// parser in the E2E browser and compare the data it returns - the node
// tree, not markup - and one page-level test checks what actually reaches
// the DOM.

import (
	"encoding/json"
	"testing"

	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// TestE2E_RichTextParser_Table pins parseRichText's output, case by case.
func TestE2E_RichTextParser_Table(t *testing.T) {
	cases := []struct {
		name, in, family, want string
	}{
		{"empty", "", "plain", `[]`},
		{"plain paragraphs keep their line breaks", "One\nstill one\n\n\nTwo", "plain",
			`[{"t":"p","c":["One",{"t":"br"},"still one"]},{"t":"p","c":["Two"]}]`},
		{"bracketed words are not BBCode", "Update [WIP] for 1.2 [beta]", "plain",
			`[{"t":"p","c":["Update [WIP] for 1.2 [beta]"]}]`},

		// BBCode
		{"bbcode bold italic underline strike", "[b]B[/b] [i]I[/i] [u]U[/u] [s]S[/s]", "bbcode",
			`[{"t":"p","c":[{"t":"strong","c":["B"]}," ",{"t":"em","c":["I"]}," ",{"t":"u","c":["U"]}," ",{"t":"s","c":["S"]}]}]`},
		{"bbcode tags are case-insensitive", "[B]x[/B]", "bbcode",
			`[{"t":"p","c":[{"t":"strong","c":["x"]}]}]`},
		{"bbcode url with a target", "See [url=https://example.com/a]the page[/url].", "bbcode",
			`[{"t":"p","c":["See ",{"t":"a","href":"https://example.com/a","c":["the page"]},"."]}]`},
		{"bbcode bare url", "[url]https://example.com[/url]", "bbcode",
			`[{"t":"p","c":[{"t":"a","href":"https://example.com/","c":["https://example.com"]}]}]`},
		{"bbcode javascript url stays text", "[url=javascript:alert(1)]x[/url]", "bbcode",
			`[{"t":"p","c":["[url=javascript:alert(1)]x[/url]"]}]`},
		{"bbcode relative url stays text", "[url=/admin]x[/url]", "bbcode",
			`[{"t":"p","c":["[url=/admin]x[/url]"]}]`},
		{"bbcode image is a link, never an img", "[img]https://i.example.com/a.png[/img]", "bbcode",
			`[{"t":"p","c":[{"t":"img","href":"https://i.example.com/a.png","alt":""}]}]`},
		{"bbcode data image stays text", "[img]data:image/png;base64,AAAA[/img]", "bbcode",
			`[{"t":"p","c":["[img]data:image/png;base64,AAAA[/img]"]}]`},
		{"bbcode linked image does not nest anchors", "[url=https://x.example][img]https://i.example/a.png[/img][/url]", "bbcode",
			`[{"t":"p","c":[{"t":"a","href":"https://x.example/","c":["Image"]}]}]`},
		{"bbcode list", "Features:\n[list]\n[*]One\n[*][b]Two[/b]\n[/list]\nAfter", "bbcode",
			`[{"t":"p","c":["Features:"]},{"t":"ul","c":[{"t":"li","c":["One"]},{"t":"li","c":[{"t":"strong","c":["Two"]}]}]},{"t":"p","c":["After"]}]`},
		{"bbcode ordered list", "[list=1][*]a[*]b[/list]", "bbcode",
			`[{"t":"ol","c":[{"t":"li","c":["a"]},{"t":"li","c":["b"]}]}]`},
		{"bbcode unclosed list still ends", "[list][*]a[*]b", "bbcode",
			`[{"t":"ul","c":[{"t":"li","c":["a"]},{"t":"li","c":["b"]}]}]`},
		{"bbcode quote", "[quote]Said\nthis[/quote]", "bbcode",
			`[{"t":"blockquote","c":[{"t":"p","c":["Said",{"t":"br"},"this"]}]}]`},
		{"bbcode code is verbatim", "[code]\n[b]not bold[/b] <x>\n[/code]", "bbcode",
			`[{"t":"pre","text":"[b]not bold[/b] <x>"}]`},
		{"bbcode styling is dropped, content kept", "[size=5][color=#ff0000][font=Comic]Big[/font][/color][/size] [center]mid[/center]", "bbcode",
			`[{"t":"p","c":["Big mid"]}]`},
		{"bbcode heading and line", "[heading]Install[/heading]\n[line]\nText", "bbcode",
			`[{"t":"h","level":1,"c":["Install"]},{"t":"hr"},{"t":"p","c":["Text"]}]`},
		{"bbcode unclosed tag is literal", "[b]never closed", "bbcode",
			`[{"t":"p","c":["[b]never closed"]}]`},
		{"bbcode stray close is literal", "text[/i] more", "bbcode",
			`[{"t":"p","c":["text[/i] more"]}]`},
		{"bbcode misnested inner unwinds as text", "[b]x [i]y[/b] z", "bbcode",
			`[{"t":"p","c":[{"t":"strong","c":["x [i]y"]}," z"]}]`},
		{"bbcode youtube is a link", "[youtube]dQw4w9WgXcQ[/youtube]", "bbcode",
			`[{"t":"p","c":[{"t":"a","href":"https://www.youtube.com/watch?v=dQw4w9WgXcQ","c":["YouTube video dQw4w9WgXcQ"]}]}]`},
		{"bbcode paragraphs split at blank lines", "[b]one[/b]\n\n\ntwo", "bbcode",
			`[{"t":"p","c":[{"t":"strong","c":["one"]}]},{"t":"p","c":["two"]}]`},

		// Markdown
		{"markdown headings map below the page's own", "# Title\n### Sub", "markdown",
			`[{"t":"h","level":1,"c":["Title"]},{"t":"h","level":3,"c":["Sub"]}]`},
		{"markdown emphasis", "**bold** and *it* and __b2__ and _i2_ and ~~gone~~ and `code`", "markdown",
			`[{"t":"p","c":[{"t":"strong","c":["bold"]}," and ",{"t":"em","c":["it"]}," and ",{"t":"strong","c":["b2"]}," and ",{"t":"em","c":["i2"]}," and ",{"t":"s","c":["gone"]}," and ",{"t":"code","c":["code"]}]}]`},
		{"markdown snake_case is not emphasis", "use my_mod_name **here**", "markdown",
			`[{"t":"p","c":["use my_mod_name ",{"t":"strong","c":["here"]}]}]`},
		{"markdown link", "Get [the **tool**](https://example.com/t) now", "markdown",
			`[{"t":"p","c":["Get ",{"t":"a","href":"https://example.com/t","c":["the ",{"t":"strong","c":["tool"]}]}," now"]}]`},
		{"markdown javascript link stays text", "[x](javascript:alert(1))", "markdown",
			`[{"t":"p","c":["[x](javascript:alert(1))"]}]`},
		{"markdown image is a link", "![shot](https://i.example/s.png)", "markdown",
			`[{"t":"p","c":[{"t":"img","href":"https://i.example/s.png","alt":"shot"}]}]`},
		{"markdown badge is one link", "[![build](https://i.example/b.svg)](https://ci.example/)", "markdown",
			`[{"t":"p","c":[{"t":"a","href":"https://ci.example/","c":["Image: build"]}]}]`},
		{"markdown lists", "- one\n- **two**\n  continued\n\n1. first\n2. second", "markdown",
			`[{"t":"ul","c":[{"t":"li","c":["one"]},{"t":"li","c":[{"t":"strong","c":["two"]},{"t":"br"},"continued"]}]},{"t":"ol","c":[{"t":"li","c":["first"]},{"t":"li","c":["second"]}]}]`},
		{"markdown quote and rule", "> quoted **text**\n> more\n\n---\nafter", "markdown",
			`[{"t":"blockquote","c":[{"t":"p","c":["quoted ",{"t":"strong","c":["text"]},{"t":"br"},"more"]}]},{"t":"hr"},{"t":"p","c":["after"]}]`},
		{"markdown fence is verbatim", "```\n**not bold** [x](https://e.example)\n```", "markdown",
			`[{"t":"pre","text":"**not bold** [x](https://e.example)"}]`},
		{"markdown unclosed fence is text", "```\nnope", "markdown",
			`[{"t":"p","c":["` + "```" + `",{"t":"br"},"nope"]}]`},
	}

	f := newE2EFixture(t)
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
	)

	inputs := make([]string, 0, len(cases))
	for _, c := range cases {
		inputs = append(inputs, c.in)
	}
	payload, err := json.Marshal(inputs)
	require.NoError(t, err)

	var got []string
	f.runInBrowser(t, chromedp.Evaluate(`(async () => {
		const { parseRichText } = await import("/static/app/richtext.js");
		return `+string(payload)+`.map((s) => JSON.stringify(parseRichText(s)));
	})()`, &got, func(p *runtime.EvaluateParams) *runtime.EvaluateParams { return p.WithAwaitPromise(true) }))
	require.Len(t, got, len(cases))

	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var doc struct {
				Family string          `json:"family"`
				Nodes  json.RawMessage `json:"nodes"`
			}
			require.NoError(t, json.Unmarshal([]byte(got[i]), &doc))
			assert.Equal(t, c.family, doc.Family)
			assert.JSONEq(t, c.want, string(doc.Nodes))
		})
	}
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_ModDescription_RendersBBCodeAsSafeElements drives the full mod
// page with a NexusMods-style description full of the markup a hostile or
// careless author could write, and asserts on the DOM the browser built.
func TestE2E_ModDescription_RendersBBCodeAsSafeElements(t *testing.T) {
	const desc = "[size=4][b]Bigger Backpacks[/b][/size]<br />" +
		"Adds [i]more[/i] room. See [url=https://www.nexusmods.com/x]the docs[/url].<br /><br />" +
		"[list][*]Works with [url=javascript:alert(1)]SkyUI[/url][*][img]https://staticdelivery.nexusmods.com/shot.png[/img][/list]" +
		"[quote]<script>alert(2)</script>Nice[/quote]"

	src := newFakeSource("fake")
	src.addMod(fakeSourceMod{
		Mod: domain.Mod{
			ID: "a", SourceID: "fake", Name: "Alpha Mod", Version: "1.0",
			Author: "Ada", Summary: "s", Description: desc,
		},
		Files: []domain.DownloadableFile{{ID: "f1", Version: "1.0"}},
	})
	f := newE2EFixtureFromSource(t, src)
	seedInstalledMod(t, f.Svc, f.Game,
		domain.Mod{ID: "a", SourceID: "fake", Name: "Alpha Mod", Version: "1.0",
			Author: "Ada", Summary: "s", Description: desc, GameID: f.Game.ID},
		true, map[string][]byte{"alpha.esp": []byte("alpha")})
	require.NoError(t, f.Svc.NewProfileManager().AddMod(t.Context(), f.Game.ID, "default",
		domain.ModReference{SourceID: "fake", ModID: "a", Version: "1.0"}))

	var got struct {
		Family   string   `json:"family"`
		Strong   []string `json:"strong"`
		Em       []string `json:"em"`
		Links    []string `json:"links"`
		Rels     []string `json:"rels"`
		Targets  []string `json:"targets"`
		Imgs     int      `json:"imgs"`
		Scripts  int      `json:"scripts"`
		Styled   int      `json:"styled"`
		Items    int      `json:"items"`
		Quote    string   `json:"quote"`
		Text     string   `json:"text"`
		NewTabSR int      `json:"newTabSR"`
	}
	f.runInBrowser(t,
		chromedp.Navigate(f.ModPagePath("fake", "a")),
		chromedp.WaitVisible(`.mod-page__description ul`, chromedp.ByQuery),
		chromedp.Evaluate(`(() => {
			const root = document.querySelector(".mod-page__description");
			const all = (sel) => Array.from(root.querySelectorAll(sel));
			return {
				family: root.dataset.family,
				strong: all("strong").map((e) => e.textContent),
				em: all("em").map((e) => e.textContent),
				links: all("a").map((e) => e.getAttribute("href")),
				rels: all("a").map((e) => e.getAttribute("rel")),
				targets: all("a").map((e) => e.getAttribute("target")),
				imgs: all("img").length,
				scripts: all("script").length,
				styled: all("[style]").length,
				items: all("li").length,
				quote: root.querySelector("blockquote")?.textContent ?? "",
				text: root.textContent,
				newTabSR: all("a .visually-hidden").length,
			};
		})()`, &got),
	)

	assert.Equal(t, "bbcode", got.Family)
	assert.Equal(t, []string{"Bigger Backpacks"}, got.Strong)
	assert.Equal(t, []string{"more"}, got.Em)
	assert.Equal(t, []string{
		"https://www.nexusmods.com/x",
		"https://staticdelivery.nexusmods.com/shot.png",
	}, got.Links, "only http(s) targets become links; the javascript: one stays text")
	for _, rel := range got.Rels {
		assert.Equal(t, "noopener noreferrer", rel)
	}
	for _, target := range got.Targets {
		assert.Equal(t, "_blank", target)
	}
	assert.Equal(t, len(got.Links), got.NewTabSR, "every link says it opens a new tab")
	assert.Zero(t, got.Imgs, "an image is a link, never an <img>")
	assert.Zero(t, got.Scripts)
	assert.Zero(t, got.Styled, "no description styling reaches the DOM")
	assert.Equal(t, 2, got.Items)
	assert.Contains(t, got.Text, "[url=javascript:alert(1)]SkyUI[/url]")
	assert.NotContains(t, got.Text, "[b]")
	assert.NotContains(t, got.Text, "[size")
	assert.Equal(t, "alert(2)Nice", got.Quote, "the cleaner strips the tag; its text stays inert text")
	assert.Empty(t, f.BrowserErrors())
}
