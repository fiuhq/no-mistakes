package steps

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/kunchenguid/no-mistakes/internal/agent"
	"github.com/kunchenguid/no-mistakes/internal/buildinfo"
	"github.com/kunchenguid/no-mistakes/internal/config"
	"github.com/kunchenguid/no-mistakes/internal/pipeline"
	"github.com/kunchenguid/no-mistakes/internal/scm"
)

const (
	testDeltaLightURL = "https://github.com/user-attachments/assets/0b1d2c3e-4f50-4a61-8b72-93a4b5c6d7e8"
	testDeltaDarkURL  = "https://github.com/user-attachments/assets/1c2e3d4f-5061-4b72-9c83-a4b5c6d7e8f9"
)

// testDeltaCard is an htr card: its colours come only from theme variables.
const testDeltaCard = `<!doctype html><html><head><style>body{background:transparent;color:var(--foreground)}.box{border:1px solid var(--border);background:var(--card);padding:12px;width:300px;height:80px}</style></head><body data-htr="architecture-delta"><div class="box">Refused row</div></body></html>`

const testDeltaJSON = `{"alt":"Before, refused rows are plain text; after, each links to its chart","changes":["CHANGED RefusedNewFlow gains chartSymbol","ADDED Refused row → ChartUrl link"]}`

func writeDelta(t *testing.T, dir, card string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, architectureDeltaHTML), []byte(card), 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeShot stands in for a browser's screenshot: a page of bg, with a block
// of ink at the top when ink is set.
func writeShot(t *testing.T, path string, bg, ink color.Color, inkRows int) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 200, 400))
	for y := 0; y < 400; y++ {
		for x := 0; x < 200; x++ {
			if ink != nil && y < inkRows && x > 10 && x < 100 {
				img.Set(x, y, ink)
			} else {
				img.Set(x, y, bg)
			}
		}
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
}

type deltaCase struct {
	sctx     *pipeline.StepContext
	step     *PRStep
	agent    *mockAgent
	uploader *stubMediaUploader
	baseSHA  string
	// result replaces the delta agent's answer, when set.
	result func() (*agent.Result, error)
	// emptyDir is a directory with no executables in it.
	emptyDir string
	// shots lists the pages the stand-in browser photographed.
	shots []string
}

// newDeltaCase is a PR step whose build requires a delta, with a stand-in
// browser and a GitHub uploader for the two images. draw runs when the delta
// agent is called, with the directory it must write into.
func newDeltaCase(t *testing.T, draw func(sctx *pipeline.StepContext, dir string)) *deltaCase {
	t.Helper()
	dir, baseSHA, headSHA := setupGitRepo(t)
	c := &deltaCase{baseSHA: baseSHA, emptyDir: t.TempDir()}
	c.agent = &mockAgent{name: "test"}
	c.sctx = newTestContextWithDBRecords(t, c.agent, dir, baseSHA, headSHA, config.Commands{})
	recordPRTestPush(t, c.sctx)
	enableDefaultEvidence(c.sctx)
	c.agent.runFn = func(ctx context.Context, opts agent.RunOpts) (*agent.Result, error) {
		if opts.Purpose != "architecture-delta" {
			return &agent.Result{Output: json.RawMessage(`{"title":"feat: demo change","body":"## What Changed\n\n- demo"}`)}, nil
		}
		draw(c.sctx, filepath.Join(c.sctx.EvidenceDir, architectureDeltaDirName))
		if c.result != nil {
			return c.result()
		}
		return &agent.Result{Output: json.RawMessage(testDeltaJSON)}, nil
	}
	c.uploader = &stubMediaUploader{t: t, urls: map[string]string{
		"architecture-delta-light.png": testDeltaLightURL,
		"architecture-delta-dark.png":  testDeltaDarkURL,
	}}
	c.step = &PRStep{mediaUploader: c.uploader, delta: architectureDeltaDeps{
		mode:    architectureDeltaRequired,
		browser: func() (string, error) { return "/stand-in/chromium", nil },
		shoot: func(_ context.Context, browser, page, out string) error {
			if browser != "/stand-in/chromium" {
				return fmt.Errorf("shot with %s", browser)
			}
			c.shots = append(c.shots, page)
			writeShot(t, out, color.White, color.Black, 40)
			return nil
		},
	}}
	return c
}

func drawGood(t *testing.T) func(*pipeline.StepContext, string) {
	return func(_ *pipeline.StepContext, dir string) { writeDelta(t, dir, testDeltaCard) }
}

const wantDeltaSection = "## Architecture delta\n\n<picture>\n<source media=\"(prefers-color-scheme: dark)\" srcset=\"" + testDeltaDarkURL + "\">\n<img alt=\"Architecture delta: Before, refused rows are plain text; after, each links to its chart\" src=\"" + testDeltaLightURL + "\">\n</picture>\n\n1. CHANGED RefusedNewFlow gains chartSymbol\n2. ADDED Refused row → ChartUrl link"

func TestArchitectureDelta_OffUnlessTheBuildRequiresIt(t *testing.T) {
	t.Parallel()
	c := newDeltaCase(t, drawGood(t))
	c.step.delta.mode = ""
	if err := c.step.drawArchitectureDelta(c.sctx, scm.ProviderGitHub, c.baseSHA); err != nil {
		t.Fatal(err)
	}
	if c.sctx.ArchitectureDelta != "" || len(c.agent.calls) != 0 || len(c.uploader.calls) != 0 {
		t.Fatalf("an unstamped build drew a delta: section=%q agent calls=%d uploads=%d", c.sctx.ArchitectureDelta, len(c.agent.calls), len(c.uploader.calls))
	}
}

func TestArchitectureDelta_OpensTheDraftedBodyAheadOfIntent(t *testing.T) {
	t.Parallel()
	c := newDeltaCase(t, drawGood(t))
	c.sctx.UserIntent = "Link refused rows to their chart."
	if err := c.step.drawArchitectureDelta(c.sctx, scm.ProviderGitHub, c.baseSHA); err != nil {
		t.Fatal(err)
	}
	if c.sctx.ArchitectureDelta != wantDeltaSection {
		t.Fatalf("section:\n%s\nwant:\n%s", c.sctx.ArchitectureDelta, wantDeltaSection)
	}
	content, err := c.step.buildPRContent(c.sctx, "feature", "main", c.baseSHA, scm.ProviderGitHub, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(content.Body, wantDeltaSection+"\n\n") {
		t.Fatalf("the delta must open the body, got:\n%s", content.Body)
	}
	delta, intent, changed := strings.Index(content.Body, "## Architecture delta"), strings.Index(content.Body, "## Intent"), strings.Index(content.Body, "## What Changed")
	if intent >= 0 && !(delta < intent) || !(delta < changed) || strings.Count(content.Body, "## Architecture delta") != 1 {
		t.Fatalf("want one delta ahead of Intent and What Changed, got:\n%s", content.Body)
	}
	if len(c.uploader.calls) != 2 || filepath.Base(c.uploader.calls[0]) != "architecture-delta-light.png" || filepath.Base(c.uploader.calls[1]) != "architecture-delta-dark.png" {
		t.Fatalf("uploads = %v, want the light image then the dark one", c.uploader.calls)
	}
	prompt := c.agent.calls[0].Prompt
	for _, want := range []string{"This prompt is that explicit request", "make no commit", "htr card", architectureDeltaMarker, "--background", "No <script>", c.baseSHA, filepath.Join(c.sctx.EvidenceDir, architectureDeltaDirName)} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("delta prompt lacks %q:\n%s", want, prompt)
		}
	}
}

func TestArchitectureDelta_OpensTheOwnedAppendix(t *testing.T) {
	t.Parallel()
	c := newDeltaCase(t, drawGood(t))
	if err := c.step.drawArchitectureDelta(c.sctx, scm.ProviderGitHub, c.baseSHA); err != nil {
		t.Fatal(err)
	}
	insertCompletedStep(t, c.sctx, "test", `{"findings":[],"summary":"","testing_summary":"ran it"}`, "")
	appendix, err := c.step.buildPRAppendix(c.sctx, scm.ProviderGitHub)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(appendix, wantDeltaSection+"\n\n") {
		t.Fatalf("a template, supplied or kept body's appendix must open with the delta, got:\n%s", appendix)
	}
}

func TestArchitectureDelta_LeadsEveryAppendixMode(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{config.PRAppendixMinimal, config.PRAppendixCollapsed} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			c := newDeltaCase(t, drawGood(t))
			c.sctx.Config.PR.Appendix = mode
			c.sctx.UserIntent = "Link refused rows to their chart."
			if err := c.step.drawArchitectureDelta(c.sctx, scm.ProviderGitHub, c.baseSHA); err != nil {
				t.Fatal(err)
			}
			content, err := c.step.buildPRContent(c.sctx, "feature", "main", c.baseSHA, scm.ProviderGitHub, 0)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(content.Body, wantDeltaSection+"\n\n") || strings.Count(content.Body, "## Architecture delta") != 1 {
				t.Fatalf("want one delta leading the body, got:\n%s", content.Body)
			}
		})
	}
}

func TestArchitectureDelta_ExecutePublishesItFirst(t *testing.T) {
	t.Parallel()
	c := newDeltaCase(t, drawGood(t))
	env, _ := fakeGH(t, "")
	c.sctx.Env = env
	if _, err := c.step.Execute(c.sctx); err != nil {
		t.Fatal(err)
	}
	if body := readPRBodyFile(t, envEntry(env, "FAKE_CLI_PR_BODY_FILE")); !strings.HasPrefix(body, wantDeltaSection+"\n\n") {
		t.Fatalf("the published body must open with the delta, got:\n%s", body)
	}
}

func TestArchitectureDelta_ExecuteFailsAndPublishesNothing(t *testing.T) {
	t.Parallel()
	c := newDeltaCase(t, func(*pipeline.StepContext, string) {})
	env, _ := fakeGH(t, "")
	c.sctx.Env = env
	if _, err := c.step.Execute(c.sctx); err == nil || !strings.Contains(err.Error(), "card was not written") {
		t.Fatalf("err = %v, want the step failed on the missing delta", err)
	}
	if data, err := os.ReadFile(envEntry(env, "FAKE_CLI_PR_BODY_FILE")); err == nil && len(data) > 0 {
		t.Fatalf("a pull request went out without its delta:\n%s", data)
	}
}

func TestArchitectureDelta_RefusalNamesOnlyWhatChangedAndStaysSmall(t *testing.T) {
	t.Parallel()
	before := "sha\n" + strings.Repeat("!! node_modules/a\n", 100000)
	if got := architectureDeltaWorktreeChange(before, before+"?? delta.html"); got != "?? delta.html" {
		t.Fatalf("want only the new path, got %d bytes: %.200q", len(got), got)
	}
	var many strings.Builder
	for i := 0; i < 500; i++ {
		fmt.Fprintf(&many, "?? f%d\n", i)
	}
	got := architectureDeltaWorktreeChange("sha", "sha\n"+many.String())
	if !strings.HasSuffix(got, "... and 490 more") || strings.Count(got, "\n") != maxArchitectureDeltaRefusalLines {
		t.Fatalf("want a bounded list, got:\n%s", got)
	}
}

func TestArchitectureDelta_ShootsTheCardOnceInEachTheme(t *testing.T) {
	t.Parallel()
	c := newDeltaCase(t, drawGood(t))
	if err := c.step.drawArchitectureDelta(c.sctx, scm.ProviderGitHub, c.baseSHA); err != nil {
		t.Fatal(err)
	}
	if len(c.shots) != 2 {
		t.Fatalf("shots = %v, want a light page and a dark page", c.shots)
	}
	for i, theme := range architectureDeltaThemes {
		data, err := os.ReadFile(c.shots[i])
		if err != nil {
			t.Fatal(err)
		}
		page := string(data)
		themeAt, cardAt := strings.Index(page, theme.css), strings.Index(page, "<style>body{background:transparent")
		if themeAt < 0 || cardAt < 0 || themeAt > cardAt {
			t.Fatalf("the %s page must supply its colours ahead of the card's own styles:\n%s", theme.name, page)
		}
		if !strings.Contains(page, architectureDeltaMarker) {
			t.Fatalf("the %s page lost the card:\n%s", theme.name, page)
		}
	}
}

func TestArchitectureDelta_TrimsTheEmptyPageUnderTheCard(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "shot.png")
	writeShot(t, path, color.RGBA{R: 13, G: 17, B: 23, A: 255}, color.White, 40)
	if err := trimArchitectureDeltaImage(path); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	cfg, err := png.DecodeConfig(f)
	if err != nil {
		t.Fatal(err)
	}
	if want := 40 + architectureDeltaMargin; cfg.Height != want || cfg.Width != 200 {
		t.Fatalf("trimmed to %dx%d, want 200x%d: the card's rows plus the margin", cfg.Width, cfg.Height, want)
	}
	blank := filepath.Join(t.TempDir(), "blank.png")
	writeShot(t, blank, color.White, nil, 0)
	if err := trimArchitectureDeltaImage(blank); err == nil || !strings.Contains(err.Error(), "drew nothing") {
		t.Fatalf("err = %v, want an empty screenshot refused", err)
	}
}

// testDeltaPolicy is the page policy the step must put first in <head>: no
// script and no load that is not inline data.
const testDeltaPolicy = `<meta http-equiv="Content-Security-Policy" content="default-src 'none'; style-src 'unsafe-inline'; img-src data:; font-src data:">`

func TestArchitectureDelta_ThemedPageOwnsItsHeadAndBackground(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		card string
		// kept is the card's text that must stay ahead of the step's head.
		kept    string
		ownHead bool
	}{
		{name: "its own head", card: `<!doctype html><html lang="en"><head><style>html,body{background:transparent}</style></head><body data-htr="architecture-delta">x</body></html>`, kept: `<!doctype html><html lang="en"><head>`, ownHead: true},
		{name: "a header and no head", card: `<html><body data-htr="architecture-delta"><header>x</header></body></html>`, kept: `<html>`},
		{name: "a doctype and no head", card: "<!DOCTYPE html>\n<body data-htr=\"architecture-delta\">x</body>", kept: "<!DOCTYPE html>\n"},
		{name: "a bare body", card: `<body data-htr="architecture-delta">x</body>`},
	}
	for _, tc := range cases {
		page := themeArchitectureDeltaCard(tc.card, architectureDeltaThemes[0])
		open := "<head>"
		if tc.ownHead {
			open = ""
		}
		if !strings.HasPrefix(page, tc.kept+open+testDeltaPolicy+"<style>") {
			t.Fatalf("%s: the policy and theme must open the head, after any doctype or html tag:\n%s", tc.name, page)
		}
		if !strings.HasSuffix(page, tc.card[len(tc.kept):]) {
			t.Fatalf("%s: the card's own text must follow unchanged:\n%s", tc.name, page)
		}
		if !strings.Contains(page, "html{background:var(--background)!important}") {
			t.Fatalf("%s: the page background must win over the card's:\n%s", tc.name, page)
		}
	}
}

// testDeltaHostileCard sets the page background transparent and carries a
// script that would paint the whole page; the step's page must win over both.
const testDeltaHostileCard = `<!doctype html><html><head><style>html,body{background:transparent;color:var(--foreground)}.box{border:1px solid var(--border);background:var(--card);padding:12px;width:300px;height:80px}</style></head><body data-htr="architecture-delta"><div class="box">Refused row</div><script>document.body.innerHTML='<div style="height:4000px;background:red"></div>'</script></body></html>`

// TestArchitectureDelta_PhotographsARealCard runs the installed Chromium, when
// there is one, on a real card in both themes.
func TestArchitectureDelta_PhotographsARealCard(t *testing.T) {
	if testing.Short() {
		t.Skip("runs a browser")
	}
	browser, err := (architectureDeltaDeps{}).findBrowser()
	if err != nil {
		t.Skipf("no Chromium on this machine: %v", err)
	}
	dir := t.TempDir()
	var sizes []image.Point
	var corners []color.Color
	for _, theme := range architectureDeltaThemes {
		page, shot := filepath.Join(dir, theme.name+".html"), filepath.Join(dir, theme.name+".png")
		if err := os.WriteFile(page, []byte(themeArchitectureDeltaCard(testDeltaHostileCard, theme)), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := (architectureDeltaDeps{}).runShoot(context.Background(), browser, page, shot); err != nil {
			t.Fatal(err)
		}
		if err := trimArchitectureDeltaImage(shot); err != nil {
			t.Fatal(err)
		}
		f, err := os.Open(shot)
		if err != nil {
			t.Fatal(err)
		}
		img, err := png.Decode(f)
		f.Close()
		if err != nil {
			t.Fatal(err)
		}
		sizes = append(sizes, img.Bounds().Size())
		corners = append(corners, img.At(img.Bounds().Max.X-1, img.Bounds().Max.Y-1))
	}
	for i, size := range sizes {
		if size.X != architectureDeltaWidth*architectureDeltaScale || size.Y >= architectureDeltaHeight*architectureDeltaScale/4 {
			t.Fatalf("%s image is %v: want the full width and the empty page trimmed", architectureDeltaThemes[i].name, size)
		}
	}
	// GitHub's light and dark page colours.
	for i, want := range []color.Color{color.RGBA{0xff, 0xff, 0xff, 0xff}, color.RGBA{0x0d, 0x11, 0x17, 0xff}} {
		if !sameColour(corners[i], want) {
			t.Fatalf("the %s page background is %v, want %v: the theme's page colour must win over the card's", architectureDeltaThemes[i].name, corners[i], want)
		}
	}
}

func TestArchitectureDelta_RedactsTheCard(t *testing.T) {
	t.Parallel()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	c := newDeltaCase(t, func(sctx *pipeline.StepContext, dir string) {
		writeDelta(t, dir, strings.Replace(testDeltaCard, "Refused row", filepath.Join(home, "secret", "plan.md")+" "+filepath.Join(sctx.WorkDir, "app.go"), 1))
	})
	if err := c.step.drawArchitectureDelta(c.sctx, scm.ProviderGitHub, c.baseSHA); err != nil {
		t.Fatal(err)
	}
	for _, page := range c.shots {
		data, err := os.ReadFile(page)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), home) || strings.Contains(string(data), c.sctx.WorkDir) {
			t.Fatalf("the photographed page still names a machine path:\n%s", data)
		}
	}
}

func TestArchitectureDelta_DrawnOncePerStep(t *testing.T) {
	t.Parallel()
	c := newDeltaCase(t, drawGood(t))
	for i := 0; i < 2; i++ {
		if err := c.step.drawArchitectureDelta(c.sctx, scm.ProviderGitHub, c.baseSHA); err != nil {
			t.Fatal(err)
		}
	}
	if len(c.agent.calls) != 1 || len(c.uploader.calls) != 2 {
		t.Fatalf("agent calls=%d uploads=%d, want 1 and 2", len(c.agent.calls), len(c.uploader.calls))
	}
}

func TestArchitectureDelta_RefusesEveryWayItCannotBeDrawn(t *testing.T) {
	t.Parallel()
	card := func(body string) func(t *testing.T) func(*pipeline.StepContext, string) {
		return func(t *testing.T) func(*pipeline.StepContext, string) {
			return func(_ *pipeline.StepContext, dir string) { writeDelta(t, dir, body) }
		}
	}
	cases := []struct {
		name    string
		draw    func(t *testing.T) func(*pipeline.StepContext, string)
		setup   func(c *deltaCase)
		wantErr string
	}{
		{name: "no browser", draw: drawGood, setup: func(c *deltaCase) {
			c.step.delta.browser = func() (string, error) {
				return findArchitectureDeltaBrowser(filepath.Join(c.emptyDir, "chromium"), c.emptyDir, nil)
			}
		}, wantErr: "NM_CHROMIUM is not a browser"},
		{name: "not attachable", draw: drawGood, setup: func(c *deltaCase) { c.sctx.Config.Test.Evidence.AttachMedia = false }, wantErr: "cannot be attached"},
		{name: "not a delta", draw: card(`<html><body><div>x</div></body></html>`), wantErr: "is not an architecture delta"},
		{name: "runs a script", draw: card(`<html><body data-htr="architecture-delta"><SCRIPT>x()</SCRIPT></body></html>`), wantErr: "may not run or fetch anything"},
		{name: "loads from the network", draw: card(`<html><body data-htr="architecture-delta"><img src="https://example.com/a.png"></body></html>`), wantErr: "may not run or fetch anything"},
		{name: "runs an event handler", draw: card(`<html><body data-htr="architecture-delta" onload="x()"></body></html>`), wantErr: "may not run or fetch anything"},
		{name: "sets its own policy or refresh", draw: card(`<html><head><meta http-equiv="refresh" content="0;url=file:///etc/passwd"></head><body data-htr="architecture-delta"></body></html>`), wantErr: "may not run or fetch anything"},
		{name: "loads a font from the network", draw: card(`<html><head><style>@font-face{src:url( '//cdn.example/f.woff')}</style></head><body data-htr="architecture-delta"></body></html>`), wantErr: "may not run or fetch anything"},
		{name: "never written", draw: func(t *testing.T) func(*pipeline.StepContext, string) {
			return func(*pipeline.StepContext, string) {}
		}, wantErr: "card was not written"},
		{name: "browser fails", draw: drawGood, setup: func(c *deltaCase) {
			c.step.delta.shoot = func(context.Context, string, string, string) error { return errors.New("crashed") }
		}, wantErr: "photograph the light architecture delta: crashed"},
		{name: "card draws nothing", draw: drawGood, setup: func(c *deltaCase) {
			c.step.delta.shoot = func(_ context.Context, _, _, out string) error {
				writeShot(t, out, color.White, nil, 0)
				return nil
			}
		}, wantErr: "the card drew nothing"},
		{name: "wrote into the worktree", draw: func(t *testing.T) func(*pipeline.StepContext, string) {
			return func(sctx *pipeline.StepContext, dir string) {
				writeDelta(t, dir, testDeltaCard)
				if err := os.WriteFile(filepath.Join(sctx.WorkDir, "delta.html"), []byte("x"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
		}, wantErr: "changed the worktree"},
		{name: "committed during the turn", draw: func(t *testing.T) func(*pipeline.StepContext, string) {
			return func(sctx *pipeline.StepContext, dir string) {
				writeDelta(t, dir, testDeltaCard)
				gitCmd(t, sctx.WorkDir, "commit", "--allow-empty", "-m", "delta")
			}
		}, wantErr: "changed the worktree"},
		{name: "wrote to an ignored path", draw: func(t *testing.T) func(*pipeline.StepContext, string) {
			return func(sctx *pipeline.StepContext, dir string) {
				writeDelta(t, dir, testDeltaCard)
				exclude := filepath.Join(sctx.WorkDir, ".git", "info", "exclude")
				if err := os.MkdirAll(filepath.Dir(exclude), 0o755); err != nil {
					t.Fatal(err)
				}
				for path, body := range map[string]string{exclude: "*.ignored\n", filepath.Join(sctx.WorkDir, "delta.ignored"): "x"} {
					if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
						t.Fatal(err)
					}
				}
			}
		}, wantErr: "changed the worktree"},
		{name: "no evidence directory", draw: drawGood, setup: func(c *deltaCase) { c.sctx.EvidenceDir = "" }, wantErr: "no evidence directory"},
		{name: "no GitHub token to attach it", draw: drawGood, setup: func(c *deltaCase) {
			c.step.mediaUploader = nil
			c.sctx.Env = []string{"PATH=" + c.emptyDir}
		}, wantErr: "no usable GitHub token"},
		{name: "agent fails", draw: drawGood, setup: func(c *deltaCase) {
			c.result = func() (*agent.Result, error) { return nil, errors.New("quota") }
		}, wantErr: "draw the architecture delta: quota"},
		{name: "agent returns nothing", draw: drawGood, setup: func(c *deltaCase) {
			c.result = func() (*agent.Result, error) { return &agent.Result{}, nil }
		}, wantErr: "returned no result"},
		{name: "agent returns unreadable JSON", draw: drawGood, setup: func(c *deltaCase) {
			c.result = func() (*agent.Result, error) { return &agent.Result{Output: json.RawMessage(`[]`)}, nil }
		}, wantErr: "unreadable JSON"},
		{name: "upload fails", draw: drawGood, setup: func(c *deltaCase) { c.uploader.err = errors.New("401") }, wantErr: "attach the architecture delta: 401"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := newDeltaCase(t, tc.draw(t))
			if tc.setup != nil {
				tc.setup(c)
			}
			err := c.step.drawArchitectureDelta(c.sctx, scm.ProviderGitHub, c.baseSHA)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.wantErr)
			}
			if c.sctx.ArchitectureDelta != "" {
				t.Fatalf("a refused delta still left a section: %q", c.sctx.ArchitectureDelta)
			}
		})
	}
}

func TestArchitectureDelta_LedgerIsBoundedAndOneLinePerEntry(t *testing.T) {
	t.Parallel()
	nine := `{"alt":"a","changes":["ADDED 1","ADDED 2","ADDED 3","ADDED 4","ADDED 5","ADDED 6","ADDED 7","ADDED 8","ADDED 9"]}`
	if _, err := parseArchitectureDelta(&agent.Result{Output: json.RawMessage(nine)}); err == nil || !strings.Contains(err.Error(), "has 9 entries") {
		t.Fatalf("err = %v, want the 8-entry budget refused", err)
	}
	for raw, want := range map[string]string{
		`{"alt":" ","changes":["ADDED a"]}`: "no alt text",
		`{"alt":"a","changes":[]}`:          "has 0 entries",
		`{"alt":"a","changes":[" "]}`:       "entry 1 is empty",
	} {
		if _, err := parseArchitectureDelta(&agent.Result{Output: json.RawMessage(raw)}); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%s: err = %v, want %q", raw, err, want)
		}
	}
	if got := oneLine(strings.Repeat("é", maxArchitectureDeltaText)); len(got) > maxArchitectureDeltaText || !utf8.ValidString(got) {
		t.Fatalf("the cap must hold and never split a character: %d bytes, valid=%v", len(got), utf8.ValidString(got))
	}
	draft, err := parseArchitectureDelta(&agent.Result{Output: json.RawMessage(`{"alt":"a \"<b>\"","changes":["ADDED a\n## Testing"]}`)})
	if err != nil {
		t.Fatal(err)
	}
	if draft.Changes[0] != "ADDED a ## Testing" {
		t.Fatalf("a ledger entry must stay on one line, got %q", draft.Changes[0])
	}
	section := renderArchitectureDelta(scm.ProviderGitHub, draft, testDeltaLightURL, testDeltaDarkURL)
	if !strings.Contains(section, `alt="Architecture delta: a &#34;&lt;b&gt;&#34;"`) {
		t.Fatalf("alt text must not close the attribute or open a tag, got:\n%s", section)
	}
}

// Not parallel: it stamps the package-global build value.
func TestArchitectureDelta_IsGeneratedOnlyOnAStampedBuild(t *testing.T) {
	body := "## What Changed\n\n- demo\n\n## Architecture delta\n\n![x](y)"
	if got := stripGeneratedSections(body); !strings.Contains(got, "Architecture delta") {
		t.Fatalf("an unstamped build must keep an author's section, got:\n%s", got)
	}
	buildinfo.ArchitectureDelta = architectureDeltaRequired
	t.Cleanup(func() { buildinfo.ArchitectureDelta = "" })
	if got := stripGeneratedSections(body); strings.Contains(got, "Architecture delta") {
		t.Fatalf("a drafted body must not carry its own delta section, got:\n%s", got)
	}
}

func TestArchitectureDelta_FindsTheBrowser(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	install := func(path string) string {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
		return path
	}
	onPath := install(filepath.Join(t.TempDir(), "chromium"))
	lookPath := func(name string) (string, error) {
		if name == "chromium" {
			return onPath, nil
		}
		return "", errors.New("not found")
	}
	if got, err := findArchitectureDeltaBrowser("", home, lookPath); err != nil || got != onPath {
		t.Fatalf("with only a chromium on PATH: got %q, %v; want %q", got, err, onPath)
	}
	older := install(filepath.Join(home, ".cache", "ms-playwright", "chromium_headless_shell-1200", "chrome-linux", "chrome-headless-shell"))
	newer := install(filepath.Join(home, ".cache", "ms-playwright", "chromium_headless_shell-1243", "chrome-linux", "chrome-headless-shell"))
	if got, err := findArchitectureDeltaBrowser("", home, lookPath); err != nil || got != newer {
		t.Fatalf("with Playwright's headless shells too: got %q, %v; want the newest %q (not %q)", got, err, newer, older)
	}
	if got, err := findArchitectureDeltaBrowser(onPath, home, lookPath); err != nil || got != onPath {
		t.Fatalf("NM_CHROMIUM must win: got %q, %v", got, err)
	}
}
