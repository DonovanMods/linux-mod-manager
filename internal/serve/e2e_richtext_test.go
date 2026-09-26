package serve_test

// #419: a mod description's BBCode or Markdown renders as a SAFE element
// tree. spa/app/richtext.js is plain JavaScript with no runtime outside a
// browser (there is no Node here, by design), so its table tests run the
// parser in the E2E browser and compare the data it returns - the node
// tree, not markup - and one page-level test checks what actually reaches
// the DOM.

import (
	"encoding/json"
	"strings"
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

		// Nesting past 32 levels is text (the review's stack overflow).
		{"bbcode nesting past the cap is text",
			strings.Repeat("[b]", 33) + "x" + strings.Repeat("[/b]", 33), "bbcode",
			`[{"t":"p","c":[` + strings.Repeat(`{"t":"strong","c":[`, 32) + `"[b]x"` +
				strings.Repeat(`]}`, 32) + `,"[/b]"]}]`},
		{"markdown quotes past the cap are text", strings.Repeat(">", 33) + " x", "markdown",
			`[` + strings.Repeat(`{"t":"blockquote","c":[`, 32) + `{"t":"p","c":["> x"]}` +
				strings.Repeat(`]}`, 32) + `]`},
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
		Titles   []string `json:"titles"`
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
				titles: all("a").map((e) => e.getAttribute("title")),
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
	assert.Equal(t, []string{"www.nexusmods.com", "staticdelivery.nexusmods.com"}, got.Titles,
		"a link's title names the host it really goes to")
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

// richTextRender is what renderInDetachedDiv reports about one input: the
// DOM RichText built for it in a detached div, or the exception it threw.
type richTextRender struct {
	Error      string `json:"error"`
	Family     string `json:"family"`
	Oversize   bool   `json:"oversize"`
	Note       string `json:"note"`
	Text       string `json:"text"`
	Depth      int    `json:"depth"`
	Imgs       int    `json:"imgs"`
	Scripts    int    `json:"scripts"`
	Styled     int    `json:"styled"`
	OnAttrs    int    `json:"onAttrs"`
	BadAnchors int    `json:"badAnchors"`
	Ms         int    `json:"ms"`
}

// renderInDetachedDiv renders each input through RichText into a detached
// div, best of three for the timing, and reports what came out.
func renderInDetachedDiv(t *testing.T, f e2eFixture, inputs []string) []richTextRender {
	t.Helper()
	payload, err := json.Marshal(inputs)
	require.NoError(t, err)
	var got []richTextRender
	f.runInBrowser(t, chromedp.Evaluate(`(async () => {
		const { RichText } = await import("/static/app/richtext.js");
		const { h, render } = await import("/static/app/render.js");
		const depthOf = (root) => {
			let max = 0;
			const stack = [[root, 0]];
			while (stack.length > 0) {
				const [el, d] = stack.pop();
				if (d > max) max = d;
				for (const child of el.children) stack.push([child, d + 1]);
			}
			return max;
		};
		return `+string(payload)+`.map((s) => {
			const r = {};
			let div;
			try {
				let best = Infinity;
				for (let run = 0; run < 3; run++) {
					div = document.createElement("div");
					const t0 = performance.now();
					render(h(RichText, { text: s }), div);
					best = Math.min(best, performance.now() - t0);
				}
				r.ms = Math.round(best);
			} catch (e) {
				r.error = String(e);
				return r;
			}
			const all = (sel) => Array.from(div.querySelectorAll(sel));
			const root = div.firstElementChild;
			r.family = root?.dataset.family ?? "";
			r.oversize = root?.dataset.oversize === "true";
			r.note = div.querySelector(".richtext__note")?.textContent ?? "";
			r.text = div.textContent.slice(0, 200);
			r.depth = depthOf(div);
			r.imgs = all("img").length;
			r.scripts = all("script").length;
			r.styled = all("[style]").length;
			r.onAttrs = all("*").filter((e) =>
				Array.from(e.attributes).some((a) => /^on/i.test(a.name))).length;
			r.badAnchors = all("a").filter((a) =>
				a.rel !== "noopener noreferrer" || a.target !== "_blank" ||
				!a.querySelector(".richtext__external") ||
				!/^https?:\/\//.test(a.getAttribute("href")) ||
				a.title !== new URL(a.href).host).length;
			return r;
		});
	})()`, &got, func(p *runtime.EvaluateParams) *runtime.EvaluateParams { return p.WithAwaitPromise(true) }))
	require.Len(t, got, len(inputs))
	return got
}

// TestE2E_RichText_HostileInputRendersInertAndNeverThrows renders the
// review's hostile battery (issue 419): every injection vector stays inert
// text, and markup nested far deeper than any description needs still
// renders - nesting past the parser's cap is literal text, so neither the
// parser nor the render recurses without bound.
func TestE2E_RichText_HostileInputRendersInertAndNeverThrows(t *testing.T) {
	rep := strings.Repeat
	cases := []struct{ name, in, family string }{
		{"bbcode javascript url", "[url=javascript:alert(1)]x[/url]", "bbcode"},
		{"bbcode javascript url after a space", "[url= javascript:alert(1)]x[/url]", "bbcode"},
		{"bbcode quoted javascript url", `[url="javascript:alert(1)"]x[/url]`, "bbcode"},
		{"bbcode javascript url body", "[url]javascript:alert(1)[/url]", "bbcode"},
		{"bbcode javascript image", "[img]javascript:alert(1)[/img]", "bbcode"},
		{"bbcode data url", "[url=data:text/html;base64,PHNjcmlwdD5hbGVydCgxKTwvc2NyaXB0Pg==]x[/url]", "bbcode"},
		{"bbcode nested links", "[b][url=https://a.example][i]x[/i][url=https://b.example]y[/url][/url][/b]", "bbcode"},
		{"bbcode unbalanced", "[b][i][u]x[/b][/u][/i][/list][*]", "bbcode"},
		{"bbcode script text", "[b]<script>alert(1)</script>[/b]", "bbcode"},
		{"bbcode img onerror text", `[quote]<img src=x onerror=alert(1)>[/quote]`, "bbcode"},
		{"bbcode colour and size", "[color=red][size=99]x[/size][/color][color=\" style=\"x]y[/color]", "bbcode"},
		{"bbcode quote and code", "[quote][url=https://a.example]q[/url][/quote][code][url=https://a.example]c[/url][/code]", "bbcode"},
		{"bbcode attribute injection", `[url=https://a.example/" onclick="alert(1)]x[/url]`, "bbcode"},
		{"bbcode attribute after a space", `[url=https://a.example/ onmouseover=alert(1)]x[/url]`, "bbcode"},
		{"bbcode rtl override", "[b]\u202eevil\u202c[/b] [url=https://a.example/\u202e]x[/url]", "bbcode"},
		{"bbcode entities", "[b]&lt;script&gt;alert(1)&lt;/script&gt;[/b]", "bbcode"},
		{"bbcode backslash url", `[url=https:\\evil.example]x[/url]`, "bbcode"},
		{"bbcode credentials url", `[url=https://nexusmods.com@evil.example]x[/url]`, "bbcode"},
		{"markdown javascript link", "# t\n[x](javascript:alert(1))", "markdown"},
		{"markdown mixed-case javascript link", "# t\n[x](JaVaScRiPt:alert(1))", "markdown"},
		{"markdown data link", "# t\n[x](data:text/html,<script>alert(1)</script>)", "markdown"},
		{"markdown reference link", "# t\n[x][1]\n\n[1]: javascript:alert(1)", "markdown"},
		{"markdown script text", "# t\n<script>alert(1)</script>\n**<img src=x onerror=alert(1)>**", "markdown"},
		{"markdown javascript image", "# t\n![a](javascript:alert(1))", "markdown"},
		{"markdown autolink", "# t\n<https://a.example> https://b.example", "markdown"},
		{"markdown nested link", "# t\n[[inner](https://a.example)](https://b.example)", "markdown"},
		{"markdown attribute injection", "# t\n[x](https://a.example\"onclick=\"alert(1))", "markdown"},

		// Depth, each under the 256 KB plain-text cap so the parser sees it;
		// the first five threw "Maximum call stack size exceeded".
		{"100 KB of nested lists", rep("[list][*]", 100000/9), "bbcode"},
		{"33k nested bold", rep("[b]", 33000) + "x" + rep("[/b]", 33000), "bbcode"},
		{"15k nested quotes", rep("[quote]", 15000) + "x" + rep("[/quote]", 15000), "bbcode"},
		{"8k nested links", rep("[url=https://a.example]", 8000) + "x" + rep("[/url]", 8000), "bbcode"},
		{"50k quote markers", rep(">", 50000) + " x", "markdown"},
		{"50k spaced quote markers", rep("> ", 50000) + "x", "markdown"},
		{"30k nested emphasis", rep("**~~", 30000) + "x" + rep("~~**", 30000), "markdown"},
	}
	f := newE2EFixture(t)
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		pollUntil(`document.querySelector('.mission-control[data-hydrated="true"]') !== null`),
	)
	inputs := make([]string, 0, len(cases))
	for _, c := range cases {
		inputs = append(inputs, c.in)
	}
	got := renderInDetachedDiv(t, f, inputs)
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := got[i]
			require.Empty(t, r.Error, "RichText never throws")
			assert.Equal(t, c.family, r.Family)
			assert.NotEmpty(t, r.Text)
			assert.LessOrEqual(t, r.Depth, 128, "the rendered tree stays shallow")
			assert.Zero(t, r.Imgs)
			assert.Zero(t, r.Scripts)
			assert.Zero(t, r.Styled)
			assert.Zero(t, r.OnAttrs)
			assert.Zero(t, r.BadAnchors)
		})
	}
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_RichText_ShapedInputRendersQuickly pins the review's slow shapes
// (issue 419): unclosed tags, unclosed code blocks, unterminated arguments
// and emphasis runs that never close each parsed in time quadratic in their
// length, and RichText parsed again on every render. Each now parses and
// renders quickly; a description over 256 KB is shown as plain text, with a
// note saying so.
//
// The budget guards the QUADRATIC regression, not absolute speed (#498).
// The quadratic shapes took about 17 s before the fix; the slowest shape
// now takes about 150 ms locally, and the worst seen on a hosted, CPU-
// contended runner under -race was 706 ms - which failed the original 500 ms
// budget three times on a runner that was merely slow. 3000 ms clears that
// with a 4x margin and still fails a quadratic regression by more than 5x.
func TestE2E_RichText_ShapedInputRendersQuickly(t *testing.T) {
	rep := strings.Repeat
	const budgetMs = 3000
	realisticBB := rep("[b]Features[/b]\n[list][*]Adds [i]more[/i] room\n[*]See [url=https://example.com/a]the docs[/url]\n[/list]\n[quote]Nice[/quote]\n\n", 12000)
	cases := []struct {
		name, in, family string
		oversize         bool
	}{
		{"40k unclosed bold", rep("[b]x", 40000), "bbcode", false},
		{"40k unclosed code", rep("[code]", 40000), "bbcode", false},
		{"40k unterminated url arguments", rep("[url=a", 40000), "plain", false},
		{"40k stray closing tags", "[b]" + rep("[/i]", 40000), "bbcode", false},
		{"40k unclosed strong runs", "# t\n" + rep("**a ", 40000), "markdown", false},
		{"40k unclosed strong runs, no heading", rep("**a ", 40000), "markdown", false},
		{"40k open brackets", "# t\n" + rep("[a", 40000), "markdown", false},
		{"40k open brackets, no heading", rep("[a", 40000), "plain", false},
		{"40k open link targets", "# t\n" + rep("[a](b", 40000), "markdown", false},
		{"20k list items with continuations", "# t\n" + rep("- a\n  b\n", 20000), "markdown", false},
		{"1 MB realistic BBCode", realisticBB, "plain", true},
		{"1 MB plain text", rep("Just some words on a line.\n", 40000), "plain", true},
	}
	f := newE2EFixture(t)
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		pollUntil(`document.querySelector('.mission-control[data-hydrated="true"]') !== null`),
	)
	inputs := make([]string, 0, len(cases))
	for _, c := range cases {
		inputs = append(inputs, c.in)
	}
	got := renderInDetachedDiv(t, f, inputs)
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := got[i]
			require.Empty(t, r.Error)
			t.Logf("%d bytes rendered in %d ms", len(c.in), r.Ms)
			assert.Less(t, r.Ms, budgetMs)
			assert.Equal(t, c.family, r.Family)
			assert.Equal(t, c.oversize, r.Oversize)
			if c.oversize {
				assert.Contains(t, r.Note, "plain text")
			} else {
				assert.Empty(t, r.Note)
			}
		})
	}
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_ModDescription_DeeplyNestedStillRendersThePage opens the full mod
// page of a mod whose description nests 20,000 bold tags (140 KB). The
// render used to overflow the stack, leaving the page stuck on "Loading
// versions…" with no description (issue 419).
func TestE2E_ModDescription_DeeplyNestedStillRendersThePage(t *testing.T) {
	desc := strings.Repeat("[b]", 20000) + "deep words" + strings.Repeat("[/b]", 20000)
	src := newFakeSource("fake")
	src.addMod(fakeSourceMod{
		Mod: domain.Mod{ID: "a", SourceID: "fake", Name: "Alpha Mod", Version: "1.0",
			Author: "Ada", Summary: "s", Description: desc},
		Files: []domain.DownloadableFile{{ID: "f1", Version: "1.0"}},
	})
	f := newE2EFixtureFromSource(t, src)
	seedInstalledMod(t, f.Svc, f.Game,
		domain.Mod{ID: "a", SourceID: "fake", Name: "Alpha Mod", Version: "1.0",
			Author: "Ada", Summary: "s", Description: desc, GameID: f.Game.ID},
		true, map[string][]byte{"alpha.esp": []byte("alpha")})
	require.NoError(t, f.Svc.NewProfileManager().AddMod(t.Context(), f.Game.ID, "default",
		domain.ModReference{SourceID: "fake", ModID: "a", Version: "1.0"}))

	var text string
	f.runInBrowser(t,
		chromedp.Navigate(f.ModPagePath("fake", "a")),
		pollUntil(`document.querySelector(".mod-page__description") !== null`),
		pollUntil(`!document.getElementById("app").innerText.includes("Loading versions")`),
		chromedp.Evaluate(`document.querySelector(".mod-page__description").textContent`, &text),
	)
	assert.Contains(t, text, "deep words")
	assert.Empty(t, f.BrowserErrors())
}
