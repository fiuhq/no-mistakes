package steps

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/kunchenguid/no-mistakes/internal/agent"
	"github.com/kunchenguid/no-mistakes/internal/buildinfo"
	"github.com/kunchenguid/no-mistakes/internal/git"
	"github.com/kunchenguid/no-mistakes/internal/pipeline"
	"github.com/kunchenguid/no-mistakes/internal/scm"
)

// An architecture delta is a diagram-design drawing of the branch's change: the
// system Before, a short Changes ledger, and the system After. A build stamped
// with buildinfo.ArchitectureDelta=required draws one on every PR-step
// execution and opens the description with it; the PR step fails when it
// cannot, so no pull request goes out without one. Any other build skips this
// entirely.
//
// The drawing is the diagram-design skill's (github.com/cathrynlavery/diagram-design):
// the agent reads the skill and its architecture-delta reference, writes the
// HTML into the run's evidence directory (never the worktree), checks it with
// the skill's own self_check.py, and exports the SVG with export_svg.py. The
// step then re-runs self_check.py itself, requires the exported SVG to be the
// delta, uploads it to GitHub user-attachments, and keeps the agent's ledger
// as text under the image so a reader that cannot open the picture still gets
// every change.

const (
	architectureDeltaRequired = "required"
	architectureDeltaDirName  = "architecture-delta"
	architectureDeltaHTML     = "architecture-delta.html"
	architectureDeltaSVG      = "architecture-delta.svg"
	// The skill's own budget: at most 8 ledger entries per delta.
	maxArchitectureDeltaChanges = 8
	maxArchitectureDeltaText    = 300
)

var architectureDeltaSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"alt": {"type": "string", "maxLength": 300},
		"changes": {"type": "array", "minItems": 1, "maxItems": 8, "items": {"type": "string", "maxLength": 300}}
	},
	"required": ["alt", "changes"],
	"additionalProperties": false
}`)

type architectureDeltaDraft struct {
	Alt     string   `json:"alt"`
	Changes []string `json:"changes"`
}

// architectureDeltaDeps are the outside world the delta needs. Zero values use
// the real skill directory and python; tests inject both.
type architectureDeltaDeps struct {
	mode     string
	skillDir func() (string, error)
	check    func(ctx context.Context, skillDir, html string) error
}

func (d architectureDeltaDeps) required() bool {
	mode := d.mode
	if mode == "" {
		mode = buildinfo.ArchitectureDelta
	}
	return mode == architectureDeltaRequired
}

// drawArchitectureDelta fills sctx.ArchitectureDelta once per PR-step
// execution, or does nothing when the build does not require a delta.
func (s *PRStep) drawArchitectureDelta(sctx *pipeline.StepContext, provider scm.Provider, baseSHA string) error {
	if !s.delta.required() || sctx.ArchitectureDelta != "" {
		return nil
	}
	if ok, why := mediaAttachEnabled(sctx, provider); !ok {
		return fmt.Errorf("an architecture delta is required on every pull request, but it cannot be attached: %s", why)
	}
	skillDir, err := s.delta.findSkill()
	if err != nil {
		return err
	}
	if strings.TrimSpace(sctx.EvidenceDir) == "" {
		return errors.New("an architecture delta is required, but this run has no evidence directory to draw it in")
	}
	dir := filepath.Join(sctx.EvidenceDir, architectureDeltaDirName)
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("clear the architecture delta directory: %w", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create the architecture delta directory: %w", err)
	}
	htmlPath := filepath.Join(dir, architectureDeltaHTML)
	svgPath := filepath.Join(dir, architectureDeltaSVG)

	ctx := sctx.Ctx
	before, err := architectureDeltaWorktreeState(ctx, sctx.WorkDir)
	if err != nil {
		return fmt.Errorf("read the worktree before drawing the architecture delta: %w", err)
	}
	sctx.Log("drawing the architecture delta...")
	result, err := sctx.RunAgentContext(ctx, agent.RunOpts{
		Prompt:     architectureDeltaPrompt(skillDir, baseSHA, sctx.Run.HeadSHA, htmlPath, svgPath) + agent.MemoryFilesRule,
		CWD:        sctx.WorkDir,
		JSONSchema: architectureDeltaSchema,
		OnChunk:    sctx.LogChunk,
		Purpose:    "architecture-delta",
	})
	if err != nil {
		return fmt.Errorf("draw the architecture delta: %w", err)
	}
	after, err := architectureDeltaWorktreeState(ctx, sctx.WorkDir)
	if err != nil {
		return fmt.Errorf("read the worktree after drawing the architecture delta: %w", err)
	}
	if after != before {
		return fmt.Errorf("drawing the architecture delta changed the worktree; the delta is drawn only under the run's evidence directory:\n%s", after)
	}
	draft, err := parseArchitectureDelta(result)
	if err != nil {
		return err
	}
	if err := s.delta.runCheck(ctx, skillDir, htmlPath); err != nil {
		return fmt.Errorf("the architecture delta fails the diagram-design self check: %w", err)
	}
	if err := requireArchitectureDeltaSVG(svgPath); err != nil {
		return err
	}
	uploader := s.resolveMediaUploader(sctx, provider)
	if uploader == nil {
		return errors.New("an architecture delta is required, but the GitHub host is not available to attach it")
	}
	url, err := uploader.UploadUserAsset(ctx, svgPath)
	if err != nil {
		return fmt.Errorf("attach the architecture delta: %w", err)
	}
	sctx.ArchitectureDelta = renderArchitectureDelta(provider, draft, url)
	sctx.Log("architecture delta attached")
	return nil
}

// architectureDeltaWorktreeState is HEAD plus every changed, untracked and
// ignored path, so a commit or a write to an ignored path shows as a change.
func architectureDeltaWorktreeState(ctx context.Context, dir string) (string, error) {
	head, err := git.Run(ctx, dir, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	status, err := git.Run(ctx, dir, "status", "--porcelain=v1", "--untracked-files=all", "--ignored")
	if err != nil {
		return "", err
	}
	return head + "\n" + status, nil
}

func architectureDeltaPrompt(skillDir, baseSHA, headSHA, htmlPath, svgPath string) string {
	return fmt.Sprintf(`Draw an architecture delta of this branch's change with the diagram-design skill.

The skill is installed at %[1]s. Read %[1]s/SKILL.md, then %[1]s/references/type-architecture-delta.md, and start from %[1]s/assets/example-architecture-delta.html. Follow the reference's layout, budget, change vocabulary and metadata contract exactly. Use the skill's current style tokens; this is a non-interactive run, so skip its first-time style question and never ask anything.

What to draw:
- Before is the system at base commit %[2]s; After is the system at %[3]s. Read the change with git diff %[2]s..%[3]s and the files it touches.
- Name the components and relationships the change actually adds, removes, changes, moves or rewires, plus the unchanged neighbours a reader needs to place them. Never invent a component the code does not have.
- A change without a topology change (a fix inside one module, docs, config) still gets a delta: the touched components marked CHANGED, with the ledger saying what changed in each.

Files:
The workspace boundary above allows writes outside the worktree only when a prompt explicitly asks for them. This prompt is that explicit request: the two files below are this run's evidence, and they belong in that evidence directory, outside the worktree.
1. Write the diagram to %[4]s. Write nothing anywhere else: do not create, edit or delete any file in the repository, and make no commit.
2. Run python3 -I %[1]s/scripts/self_check.py %[4]s and fix the diagram until it passes.
3. Run python3 -I %[1]s/scripts/export_svg.py --system-fonts %[4]s %[5]s.

Return JSON:
- alt: one sentence describing the delta for a reader who cannot see the image.
- changes: the Changes ledger, one entry per line in the diagram's order, each starting with its uppercase change word (ADDED, REMOVED, CHANGED, MOVED or REWIRED).
`, skillDir, baseSHA, headSHA, htmlPath, svgPath)
}

func parseArchitectureDelta(result *agent.Result) (architectureDeltaDraft, error) {
	var draft architectureDeltaDraft
	if result == nil || result.Output == nil {
		return draft, errors.New("the architecture delta agent returned no result")
	}
	if err := json.Unmarshal(result.Output, &draft); err != nil {
		return draft, fmt.Errorf("the architecture delta agent returned unreadable JSON: %w", err)
	}
	draft.Alt = oneLine(draft.Alt)
	if draft.Alt == "" {
		return draft, errors.New("the architecture delta agent returned no alt text")
	}
	if len(draft.Changes) == 0 || len(draft.Changes) > maxArchitectureDeltaChanges {
		return draft, fmt.Errorf("the architecture delta ledger has %d entries; the skill allows 1 to %d", len(draft.Changes), maxArchitectureDeltaChanges)
	}
	for i, change := range draft.Changes {
		draft.Changes[i] = oneLine(change)
		if draft.Changes[i] == "" {
			return draft, fmt.Errorf("architecture delta ledger entry %d is empty", i+1)
		}
	}
	return draft, nil
}

// oneLine keeps agent text to one bounded markdown line.
func oneLine(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	if len(text) > maxArchitectureDeltaText {
		cut := maxArchitectureDeltaText
		for cut > 0 && !utf8.RuneStart(text[cut]) {
			cut--
		}
		text = text[:cut]
	}
	return text
}

func requireArchitectureDeltaSVG(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("the architecture delta was not exported to SVG: %w", err)
	}
	if !bytes.Contains(data, []byte(`data-diagram="architecture-delta"`)) {
		return errors.New(`the exported SVG is not an architecture delta: it carries no data-diagram="architecture-delta"`)
	}
	return nil
}

func renderArchitectureDelta(provider scm.Provider, draft architectureDeltaDraft, url string) string {
	var b strings.Builder
	b.WriteString("## Architecture delta\n\n")
	fmt.Fprintf(&b, "![Architecture delta: %s](%s)\n\n", markdownAltText(draft.Alt), url)
	for i, change := range draft.Changes {
		fmt.Fprintf(&b, "%d. %s\n", i+1, change)
	}
	return neutralizeAttestationMarkers(provider, strings.TrimRight(b.String(), "\n"))
}

// withArchitectureDelta puts the drawn delta first, ahead of Intent.
func withArchitectureDelta(sctx *pipeline.StepContext, body string) string {
	if sctx == nil || sctx.ArchitectureDelta == "" {
		return body
	}
	if strings.TrimSpace(body) == "" {
		return sctx.ArchitectureDelta
	}
	return sctx.ArchitectureDelta + "\n\n" + body
}

func (d architectureDeltaDeps) findSkill() (string, error) {
	if d.skillDir != nil {
		return d.skillDir()
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("an architecture delta is required, but the home directory is unknown: %w", err)
	}
	return d.findSkillIn(home)
}

// findSkillIn looks where the skills CLI and Claude Code install skills under home.
func (architectureDeltaDeps) findSkillIn(home string) (string, error) {
	candidates := []string{
		filepath.Join(home, ".agents", "skills", "diagram-design"),
		filepath.Join(home, ".claude", "skills", "diagram-design"),
	}
	for _, dir := range candidates {
		if _, err := os.Stat(filepath.Join(dir, "SKILL.md")); err == nil {
			return dir, nil
		}
	}
	return "", fmt.Errorf("an architecture delta is required, but the diagram-design skill is not installed (looked in %s); install it with `npx skills add cathrynlavery/diagram-design`", strings.Join(candidates, " and "))
}

func (d architectureDeltaDeps) runCheck(ctx context.Context, skillDir, html string) error {
	if d.check != nil {
		return d.check(ctx, skillDir, html)
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		return fmt.Errorf("python3 is not on PATH: %w", err)
	}
	out, err := exec.CommandContext(ctx, python, "-I", filepath.Join(skillDir, "scripts", "self_check.py"), html).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
