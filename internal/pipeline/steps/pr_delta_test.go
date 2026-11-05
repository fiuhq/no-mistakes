package steps

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/agent"
	"github.com/kunchenguid/no-mistakes/internal/config"
	"github.com/kunchenguid/no-mistakes/internal/pipeline"
	"github.com/kunchenguid/no-mistakes/internal/scm"
)

const testDeltaURL = "https://github.com/user-attachments/assets/0b1d2c3e-4f50-4a61-8b72-93a4b5c6d7e8"

const testDeltaSVG = `<svg xmlns="http://www.w3.org/2000/svg" data-diagram="architecture-delta" viewBox="0 0 10 10"><title>delta</title></svg>`

const testDeltaJSON = `{"title":"Refused rows link to their chart","alt":"Before, refused rows are plain text; after, each links to its chart","changes":["CHANGED RefusedNewFlow gains chartSymbol","ADDED Refused row → ChartUrl link"]}`

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
}

// newDeltaCase is a PR step whose build requires a delta, with a skill dir,
// a passing self check and a GitHub uploader for the SVG. draw runs when the
// delta agent is called, with the directory it must write into.
func newDeltaCase(t *testing.T, draw func(sctx *pipeline.StepContext, dir string)) *deltaCase {
	t.Helper()
	dir, baseSHA, headSHA := setupGitRepo(t)
	c := &deltaCase{baseSHA: baseSHA}
	c.agent = &mockAgent{name: "test"}
	c.sctx = newTestContextWithDBRecords(t, c.agent, dir, baseSHA, headSHA, config.Commands{})
	enableDefaultEvidence(c.sctx)
	c.agent.runFn = func(ctx context.Context, opts agent.RunOpts) (*agent.Result, error) {
		if opts.Purpose != "architecture-delta" {
			return &agent.Result{Output: json.RawMessage(`{"title":"feat: demo change","body":"## What Changed\n\n- demo"}`)}, nil
		}
		draw(c.sctx, filepath.Join(c.sctx.EvidenceDir, architectureDeltaDirName))
		return &agent.Result{Output: json.RawMessage(testDeltaJSON)}, nil
	}
	c.uploader = &stubMediaUploader{t: t, urls: map[string]string{architectureDeltaSVG: testDeltaURL}}
	skill := t.TempDir()
	c.step = &PRStep{mediaUploader: c.uploader, delta: architectureDeltaDeps{
		mode:     architectureDeltaRequired,
		skillDir: func() (string, error) { return skill, nil },
		check:    func(context.Context, string, string) error { return nil },
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
	for _, want := range []string{"references/type-architecture-delta.md", "scripts/self_check.py", "export_svg.py --system-fonts", c.baseSHA, filepath.Join(c.sctx.EvidenceDir, architectureDeltaDirName)} {
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
	nine := `{"title":"t","alt":"a","changes":["ADDED 1","ADDED 2","ADDED 3","ADDED 4","ADDED 5","ADDED 6","ADDED 7","ADDED 8","ADDED 9"]}`
	if _, err := parseArchitectureDelta(&agent.Result{Output: json.RawMessage(nine)}); err == nil || !strings.Contains(err.Error(), "has 9 entries") {
		t.Fatalf("err = %v, want the 8-entry budget refused", err)
	}
	draft, err := parseArchitectureDelta(&agent.Result{Output: json.RawMessage(`{"title":" t ","alt":"a [x]","changes":["ADDED a\n## Testing"]}`)})
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

func TestArchitectureDelta_IsAGeneratedSection(t *testing.T) {
	t.Parallel()
	body := "## What Changed\n\n- demo\n\n## Architecture delta\n\n![x](y)"
	if got := stripGeneratedSections(body); strings.Contains(got, "Architecture delta") {
		t.Fatalf("a drafted body must not carry its own delta section, got:\n%s", got)
	}
}
