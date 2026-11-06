package steps

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/kunchenguid/no-mistakes/internal/agent"
	"github.com/kunchenguid/no-mistakes/internal/buildinfo"
	"github.com/kunchenguid/no-mistakes/internal/config"
	"github.com/kunchenguid/no-mistakes/internal/pipeline"
	"github.com/kunchenguid/no-mistakes/internal/pipeline/steps/internal/stepstest"
	"github.com/kunchenguid/no-mistakes/internal/scm"
)

const testDeltaURL = "https://github.com/user-attachments/assets/0b1d2c3e-4f50-4a61-8b72-93a4b5c6d7e8"

const testDeltaSVG = `<svg xmlns="http://www.w3.org/2000/svg" data-diagram="architecture-delta" viewBox="0 0 10 10"><title>delta</title></svg>`

const testDeltaJSON = `{"alt":"Before, refused rows are plain text; after, each links to its chart","changes":["CHANGED RefusedNewFlow gains chartSymbol","ADDED Refused row → ChartUrl link"]}`

func writeDelta(t *testing.T, dir, svg string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{architectureDeltaHTML: "<html></html>", architectureDeltaSVG: svg} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
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
}

// newDeltaCase is a PR step whose build requires a delta, with a skill dir,
// a passing self check and a GitHub uploader for the SVG. draw runs when the
// delta agent is called, with the directory it must write into.
func newDeltaCase(t *testing.T, draw func(sctx *pipeline.StepContext, dir string)) *deltaCase {
	t.Helper()
	dir, baseSHA, headSHA := setupGitRepo(t)
	c := &deltaCase{baseSHA: baseSHA, emptyDir: t.TempDir()}
	c.agent = &mockAgent{name: "test"}
	c.sctx = newTestContextWithDBRecords(t, c.agent, dir, baseSHA, headSHA, config.Commands{})
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
	c.uploader = &stubMediaUploader{t: t, urls: map[string]string{architectureDeltaSVG: testDeltaURL}}
	skill := t.TempDir()
	c.step = &PRStep{mediaUploader: c.uploader, delta: architectureDeltaDeps{
		mode:     architectureDeltaRequired,
		skillDir: func() (string, error) { return skill, nil },
		check: func(_ context.Context, skillDir, html string) error {
			if want := filepath.Join(c.sctx.EvidenceDir, architectureDeltaDirName, architectureDeltaHTML); skillDir != skill || html != want {
				return fmt.Errorf("checked %s with %s, want %s with %s", html, skillDir, want, skill)
			}
			return nil
		},
	}}
	return c
}

func drawGood(t *testing.T) func(*pipeline.StepContext, string) {
	return func(_ *pipeline.StepContext, dir string) { writeDelta(t, dir, testDeltaSVG) }
}

const wantDeltaSection = "## Architecture delta\n\n![Architecture delta: Before, refused rows are plain text; after, each links to its chart](" + testDeltaURL + ")\n\n1. CHANGED RefusedNewFlow gains chartSymbol\n2. ADDED Refused row → ChartUrl link"

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
	if len(c.uploader.calls) != 1 || filepath.Base(c.uploader.calls[0]) != architectureDeltaSVG {
		t.Fatalf("uploads = %v, want the one SVG", c.uploader.calls)
	}
	prompt := c.agent.calls[0].Prompt
	for _, want := range []string{"This prompt is that explicit request", "make no commit", "references/type-architecture-delta.md", "scripts/self_check.py", "export_svg.py --system-fonts", c.baseSHA, filepath.Join(c.sctx.EvidenceDir, architectureDeltaDirName)} {
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
	if _, err := c.step.Execute(c.sctx); err == nil || !strings.Contains(err.Error(), "was not exported to SVG") {
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

func TestArchitectureDelta_RunsTheSkillsSelfCheck(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in python3 is a POSIX script")
	}
	t.Parallel()
	for name, tc := range map[string]struct {
		script  string
		wantErr string
	}{
		"passes":   {script: "#!/bin/sh\n[ \"$1 $2 $3\" = \"-I /skill/scripts/self_check.py /run/delta.html\" ] || { echo \"wrong args: $*\"; exit 2; }\necho OK\n"},
		"fails it": {script: "#!/bin/sh\necho 'painted outside viewBox'\nexit 3\n", wantErr: "painted outside viewBox"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			c := newDeltaCase(t, drawGood(t))
			bin := t.TempDir()
			stepstest.WriteExecutable(t, filepath.Join(bin, "python3"), []byte(tc.script))
			c.sctx.Env = []string{"PATH=" + bin + ":/usr/bin:/bin"}
			err := architectureDeltaDeps{}.runCheck(c.sctx, "/skill", "/run/delta.html")
			if tc.wantErr == "" && err != nil {
				t.Fatal(err)
			}
			if tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}

func TestArchitectureDelta_RedactsTheAttachedSVG(t *testing.T) {
	t.Parallel()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	var svgPath string
	c := newDeltaCase(t, func(sctx *pipeline.StepContext, dir string) {
		svgPath = filepath.Join(dir, architectureDeltaSVG)
		writeDelta(t, dir, strings.Replace(testDeltaSVG, "<title>delta</title>", "<title>"+filepath.Join(home, "secret", "plan.md")+" "+filepath.Join(sctx.WorkDir, "app.go")+"</title>", 1))
	})
	if err := c.step.drawArchitectureDelta(c.sctx, scm.ProviderGitHub, c.baseSHA); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(svgPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), home) || strings.Contains(string(data), c.sctx.WorkDir) {
		t.Fatalf("the attachment still names a machine path:\n%s", data)
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
	if len(c.agent.calls) != 1 || len(c.uploader.calls) != 1 {
		t.Fatalf("agent calls=%d uploads=%d, want 1 each", len(c.agent.calls), len(c.uploader.calls))
	}
}

func TestArchitectureDelta_RefusesEveryWayItCannotBeDrawn(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		draw    func(t *testing.T) func(*pipeline.StepContext, string)
		setup   func(c *deltaCase)
		wantErr string
	}{
		{name: "no skill", draw: drawGood, setup: func(c *deltaCase) {
			c.step.delta.skillDir = func() (string, error) { return architectureDeltaDeps{}.findSkillIn(t.TempDir()) }
		}, wantErr: "npx skills add cathrynlavery/diagram-design"},
		{name: "not attachable", draw: drawGood, setup: func(c *deltaCase) { c.sctx.Config.Test.Evidence.AttachMedia = false }, wantErr: "cannot be attached"},
		{name: "self check fails", draw: drawGood, setup: func(c *deltaCase) {
			c.step.delta.check = func(context.Context, string, string) error { return errors.New("viewBox overflow") }
		}, wantErr: "fails the diagram-design self check: viewBox overflow"},
		{name: "not a delta", draw: func(t *testing.T) func(*pipeline.StepContext, string) {
			return func(_ *pipeline.StepContext, dir string) {
				writeDelta(t, dir, `<svg data-diagram="architecture"></svg>`)
			}
		}, wantErr: "is not an architecture delta"},
		{name: "never exported", draw: func(t *testing.T) func(*pipeline.StepContext, string) {
			return func(*pipeline.StepContext, string) {}
		}, wantErr: "was not exported to SVG"},
		{name: "wrote into the worktree", draw: func(t *testing.T) func(*pipeline.StepContext, string) {
			return func(sctx *pipeline.StepContext, dir string) {
				writeDelta(t, dir, testDeltaSVG)
				if err := os.WriteFile(filepath.Join(sctx.WorkDir, "delta.html"), []byte("x"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
		}, wantErr: "changed the worktree"},
		{name: "committed during the turn", draw: func(t *testing.T) func(*pipeline.StepContext, string) {
			return func(sctx *pipeline.StepContext, dir string) {
				writeDelta(t, dir, testDeltaSVG)
				gitCmd(t, sctx.WorkDir, "commit", "--allow-empty", "-m", "delta")
			}
		}, wantErr: "changed the worktree"},
		{name: "wrote to an ignored path", draw: func(t *testing.T) func(*pipeline.StepContext, string) {
			return func(sctx *pipeline.StepContext, dir string) {
				writeDelta(t, dir, testDeltaSVG)
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
	draft, err := parseArchitectureDelta(&agent.Result{Output: json.RawMessage(`{"alt":"a [x]","changes":["ADDED a\n## Testing"]}`)})
	if err != nil {
		t.Fatal(err)
	}
	if draft.Changes[0] != "ADDED a ## Testing" {
		t.Fatalf("a ledger entry must stay on one line, got %q", draft.Changes[0])
	}
	section := renderArchitectureDelta(scm.ProviderGitHub, draft, testDeltaURL)
	if !strings.Contains(section, "![Architecture delta: a (x)](") {
		t.Fatalf("alt text must not close the image brackets, got:\n%s", section)
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
