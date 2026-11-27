package steps

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/kunchenguid/no-mistakes/internal/agent"
	"github.com/kunchenguid/no-mistakes/internal/buildinfo"
	"github.com/kunchenguid/no-mistakes/internal/git"
	"github.com/kunchenguid/no-mistakes/internal/pipeline"
	"github.com/kunchenguid/no-mistakes/internal/safepath"
	"github.com/kunchenguid/no-mistakes/internal/scm"
	"github.com/kunchenguid/no-mistakes/internal/shellenv"
	"github.com/kunchenguid/no-mistakes/internal/winproc"
)

// An architecture delta is an htr card of the branch's change: the system
// Before and After side by side, and a short list of the changes. A build
// stamped with buildinfo.ArchitectureDelta=required draws one on every PR-step
// execution and opens the description with it; the PR step fails when it
// cannot, so no pull request goes out without one. Any other build skips this
// entirely.
//
// An htr card is plain HTML and CSS that takes its colours from theme
// variables (--background, --foreground, --card, --border, --primary ...),
// the way a Paseo html_render card does. The agent writes the card into the
// run's evidence directory (never the worktree). The step then supplies the
// variables itself, once with light colours and once with dark, screenshots
// each page in a headless Chromium with the network shut, trims the empty
// space under the card, uploads both images, and publishes them in one
// <picture> that follows the reader's light or dark theme. The change list
// stays as text under the image, so a reader who cannot open the picture
// still gets every change.

const (
	architectureDeltaRequired = "required"
	// A dot name of its own, so it never meets a folder the Test step made.
	architectureDeltaDirName = ".pr-architecture-delta"
	architectureDeltaHTML    = "architecture-delta.html"
	// The card's marker: the agent's file must say it is this card.
	architectureDeltaMarker = `data-htr="architecture-delta"`
	// At most 8 change entries per delta.
	maxArchitectureDeltaChanges = 8
	maxArchitectureDeltaText    = 300
	// How many changed worktree paths a refusal names. The state it compares
	// lists every ignored file, and a step error travels in one IPC frame.
	maxArchitectureDeltaRefusalLines = 10
	// The page the card is photographed in, in CSS pixels, and its scale.
	// The page is taller than any card; the empty space under it is trimmed.
	architectureDeltaWidth  = 1200
	architectureDeltaHeight = 4000
	architectureDeltaScale  = 2
	// Space kept under the card when trimming, in image pixels.
	architectureDeltaMargin = 24 * architectureDeltaScale
)

// architectureDeltaTheme is one of the two colour sets the card is shot in.
type architectureDeltaTheme struct {
	name string
	css  string
}

// The colours are GitHub's, so each image sits on the page it is read on.
var architectureDeltaThemes = []architectureDeltaTheme{
	{name: "light", css: `color-scheme:light;--background:#ffffff;--foreground:#1f2328;--muted-foreground:#59636e;--border:#d1d9e0;--card:#f6f8fa;--accent:#eaeef2;--primary:#0969da;--primary-foreground:#ffffff;--ring:#0969da;--chart-1:#0969da;--chart-2:#1a7f37;--chart-3:#9a6700;--chart-4:#cf222e;--chart-5:#8250df;--chart-6:#bf3989`},
	{name: "dark", css: `color-scheme:dark;--background:#0d1117;--foreground:#e6edf3;--muted-foreground:#9198a1;--border:#3d444d;--card:#151b23;--accent:#212830;--primary:#4493f8;--primary-foreground:#0d1117;--ring:#4493f8;--chart-1:#4493f8;--chart-2:#3fb950;--chart-3:#d29922;--chart-4:#f85149;--chart-5:#ab7df8;--chart-6:#db61a2`},
}

const architectureDeltaShared = `--radius:10px;--font-sans:-apple-system,BlinkMacSystemFont,"Segoe UI","Noto Sans",Helvetica,Arial,sans-serif;--font-mono:ui-monospace,SFMono-Regular,"SF Mono",Menlo,Consolas,monospace`

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

// architectureDeltaDeps are the outside world the delta needs. Zero values
// find a real Chromium and run it; tests inject both.
type architectureDeltaDeps struct {
	mode    string
	browser func() (string, error)
	shoot   func(ctx context.Context, browser, page, png string) error
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
	browser, err := s.delta.findBrowser()
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

	ctx := sctx.Ctx
	before, err := architectureDeltaWorktreeState(ctx, sctx.WorkDir)
	if err != nil {
		return fmt.Errorf("read the worktree before drawing the architecture delta: %w", err)
	}
	sctx.Log("drawing the architecture delta...")
	result, err := sctx.RunAgentContext(ctx, agent.RunOpts{
		Prompt:     architectureDeltaPrompt(baseSHA, sctx.Run.HeadSHA, htmlPath) + agent.MemoryFilesRule,
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
		return fmt.Errorf("drawing the architecture delta changed the worktree; the delta is drawn only under the run's evidence directory:\n%s", architectureDeltaWorktreeChange(before, after))
	}
	draft, err := parseArchitectureDelta(result)
	if err != nil {
		return err
	}
	card, err := readArchitectureDeltaCard(htmlPath)
	if err != nil {
		return err
	}
	card = safepath.RedactText(safepath.RedactWorktrees(card, sctx.WorkDir, nmWorktreesRoot()))
	uploader := s.resolveMediaUploader(sctx, provider)
	if uploader == nil {
		return errors.New("an architecture delta is required, but the GitHub host is not available to attach it")
	}
	urls := make(map[string]string, len(architectureDeltaThemes))
	for _, theme := range architectureDeltaThemes {
		page := filepath.Join(dir, "architecture-delta-"+theme.name+".html")
		png := filepath.Join(dir, "architecture-delta-"+theme.name+".png")
		if err := os.WriteFile(page, []byte(themeArchitectureDeltaCard(card, theme)), 0o644); err != nil {
			return fmt.Errorf("write the %s architecture delta page: %w", theme.name, err)
		}
		if err := s.delta.runShoot(ctx, browser, page, png); err != nil {
			return fmt.Errorf("photograph the %s architecture delta: %w", theme.name, err)
		}
		if err := trimArchitectureDeltaImage(png); err != nil {
			return fmt.Errorf("trim the %s architecture delta: %w", theme.name, err)
		}
		url, err := uploader.UploadUserAsset(ctx, png)
		if err != nil {
			return fmt.Errorf("attach the architecture delta: %w", err)
		}
		urls[theme.name] = url
	}
	sctx.ArchitectureDelta = renderArchitectureDelta(provider, draft, urls["light"], urls["dark"])
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

// architectureDeltaWorktreeChange names the lines that differ between two
// worktree states, bounded, so a refusal shows what moved and stays small.
func architectureDeltaWorktreeChange(before, after string) string {
	seen := make(map[string]int)
	for _, line := range nonEmptyLines(before) {
		seen[line]++
	}
	var changed []string
	for _, line := range nonEmptyLines(after) {
		if seen[line] > 0 {
			seen[line]--
			continue
		}
		changed = append(changed, line)
	}
	for _, line := range nonEmptyLines(before) {
		if seen[line] > 0 {
			seen[line]--
			changed = append(changed, line)
		}
	}
	if extra := len(changed) - maxArchitectureDeltaRefusalLines; extra > 0 {
		changed = append(changed[:maxArchitectureDeltaRefusalLines], fmt.Sprintf("... and %d more", extra))
	}
	return strings.Join(changed, "\n")
}

func nonEmptyLines(text string) []string {
	return strings.FieldsFunc(text, func(r rune) bool { return r == '\n' })
}

func architectureDeltaPrompt(baseSHA, headSHA, htmlPath string) string {
	return fmt.Sprintf(`Draw an architecture delta of this branch's change as one htr card: a self-contained HTML page that shows the system Before and After side by side, with the changes listed under them.

What to draw:
- Before is the system at base commit %[1]s; After is the system at %[2]s. Read the change with git diff %[1]s..%[2]s and the files it touches.
- Name the components and relationships the change actually adds, removes, changes, moves or rewires, plus the unchanged neighbours a reader needs to place them. Never invent a component the code does not have.
- A change without a topology change (a fix inside one module, docs, config) still gets a card: the touched components marked as changed, with the list saying what changed in each.
- Keep labels short and in everyday words. Before and After are two columns of boxes and arrows built from HTML and CSS (grid, flex, borders); what changed is marked with a small coloured tint and a word (added, removed, changed, moved, rewired).

How the card is built:
- One complete HTML document. Put %[4]s on its <body>.
- Colours and fonts come only from these CSS variables, which the page supplies in a light and a dark version: --background, --foreground, --muted-foreground, --border, --card, --accent, --primary, --primary-foreground, --ring, --chart-1 to --chart-6, --radius, --font-sans, --font-mono. Never set a fixed colour for text, boxes or the page; color-mix() of a variable with transparent is fine for a tint.
- Leave the page background to the page: do not set a background on html or body. Keep the card at most 1200 CSS pixels wide.
- No <script>, no inline event handler (onload= and the like), no <iframe>, <object>, <embed>, <link>, <base> or <meta http-equiv>, no @import, and nothing loaded from the network (no http or https URL in src, srcset or url()). Inline SVG and inline styles are fine.

Files:
The workspace boundary above allows writes outside the worktree only when a prompt explicitly asks for them. This prompt is that explicit request: the file below is this run's evidence, and it belongs in that evidence directory, outside the worktree.
Write the card to %[3]s. Write nothing anywhere else: do not create, edit or delete any file in the repository, and make no commit.

Return JSON:
- alt: one sentence describing the card for a reader who cannot see the image.
- changes: the changes, one entry per line in the card's order, each starting with its uppercase change word (ADDED, REMOVED, CHANGED, MOVED or REWIRED).
`, baseSHA, headSHA, htmlPath, architectureDeltaMarker)
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
		return draft, fmt.Errorf("the architecture delta ledger has %d entries; a delta lists 1 to %d changes", len(draft.Changes), maxArchitectureDeltaChanges)
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

// architectureDeltaForbidden is an early, readable refusal of the common ways
// a card could run, embed another document or reach the network. The
// enforcement is architectureDeltaCSP, which the browser applies however the
// card spells things.
var architectureDeltaForbidden = regexp.MustCompile(`(?i)<\s*(script|iframe|object|embed|link|base)\b|@import|javascript:|http-equiv|\son[a-z]+\s*=|(?:src|srcset)\s*=\s*["']?\s*(?:https?:)?//|url\(\s*["']?\s*(?:https?:)?//`)

// architectureDeltaCSP stops every script and every load that is not inline
// data, so the photographed page is exactly the redacted card.
const architectureDeltaCSP = `<meta http-equiv="Content-Security-Policy" content="default-src 'none'; style-src 'unsafe-inline'; img-src data:; font-src data:">`

// readArchitectureDeltaCard reads the agent's card and refuses one that is
// not the delta or carries something the page must not run or fetch.
func readArchitectureDeltaCard(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("the architecture delta card was not written: %w", err)
	}
	card := string(data)
	if !strings.Contains(card, architectureDeltaMarker) {
		return "", fmt.Errorf("the card is not an architecture delta: it carries no %s", architectureDeltaMarker)
	}
	if m := architectureDeltaForbidden.FindString(card); m != "" {
		return "", fmt.Errorf("the architecture delta card may not run or fetch anything, but it has %q", m)
	}
	return card, nil
}

var (
	architectureDeltaHead     = regexp.MustCompile(`(?i)<head(?:\s[^>]*)?>`)
	architectureDeltaPreamble = regexp.MustCompile(`(?i)^\s*(?:<!doctype[^>]*>\s*)?(?:<html(?:\s[^>]*)?>)?`)
)

// themeArchitectureDeltaCard gives the card its colours, as Paseo gives an
// html_render card its theme: the variables on :root, ahead of the card's own
// styles, and a solid page background the trim can find the card's edge on.
// The policy comes first in <head>, ahead of anything of the card's.
func themeArchitectureDeltaCard(card string, theme architectureDeltaTheme) string {
	head := architectureDeltaCSP + "<style>:root{" + theme.css + ";" + architectureDeltaShared + "}html{background:var(--background)!important}body{margin:0;padding:24px}</style>"
	if loc := architectureDeltaHead.FindStringIndex(card); loc != nil {
		return card[:loc[1]] + head + card[loc[1]:]
	}
	at := architectureDeltaPreamble.FindStringIndex(card)[1]
	return card[:at] + "<head>" + head + "</head>" + card[at:]
}

// trimArchitectureDeltaImage cuts the empty page under the card, keeping a
// small margin. The page background is the bottom-right pixel's colour.
func trimArchitectureDeltaImage(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	img, err := png.Decode(f)
	f.Close()
	if err != nil {
		return fmt.Errorf("read the screenshot: %w", err)
	}
	bounds := img.Bounds()
	bg := img.At(bounds.Max.X-1, bounds.Max.Y-1)
	last := -1
	for y := bounds.Max.Y - 1; y >= bounds.Min.Y && last < 0; y-- {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			if !sameColour(img.At(x, y), bg) {
				last = y
				break
			}
		}
	}
	if last < 0 {
		return errors.New("the screenshot is empty: the card drew nothing")
	}
	bottom := min(last+1+architectureDeltaMargin, bounds.Max.Y)
	cropped := image.NewRGBA(image.Rect(0, 0, bounds.Dx(), bottom-bounds.Min.Y))
	draw.Draw(cropped, cropped.Bounds(), img, bounds.Min, draw.Src)
	out, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := png.Encode(out, cropped); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// sameColour is equality with a little room for antialiasing noise.
func sameColour(a, b color.Color) bool {
	ar, ag, ab, aa := a.RGBA()
	br, bg, bb, ba := b.RGBA()
	near := func(x, y uint32) bool { d := int64(x) - int64(y); return d < 0x300 && d > -0x300 }
	return near(ar, br) && near(ag, bg) && near(ab, bb) && near(aa, ba)
}

func renderArchitectureDelta(provider scm.Provider, draft architectureDeltaDraft, lightURL, darkURL string) string {
	var b strings.Builder
	b.WriteString("## Architecture delta\n\n")
	alt := html.EscapeString("Architecture delta: " + draft.Alt)
	fmt.Fprintf(&b, "<picture>\n<source media=\"(prefers-color-scheme: dark)\" srcset=\"%s\">\n<img alt=\"%s\" src=\"%s\">\n</picture>\n\n", html.EscapeString(darkURL), alt, html.EscapeString(lightURL))
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

// architectureDeltaExcludedDirs keeps a stamped build's drawing off the evidence
// branch. An unstamped build draws nothing, so it excludes nothing.
func architectureDeltaExcludedDirs() []string {
	if (architectureDeltaDeps{}).required() {
		return []string{architectureDeltaDirName}
	}
	return nil
}

func (d architectureDeltaDeps) findBrowser() (string, error) {
	if d.browser != nil {
		return d.browser()
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("an architecture delta is required, but the home directory is unknown: %w", err)
	}
	return findArchitectureDeltaBrowser(os.Getenv("NM_CHROMIUM"), home, exec.LookPath)
}

// findArchitectureDeltaBrowser picks the Chromium that photographs the card:
// NM_CHROMIUM when set, then Playwright's headless shell, then a Chromium or
// Chrome on PATH or in /Applications.
func findArchitectureDeltaBrowser(override, home string, lookPath func(string) (string, error)) (string, error) {
	if override != "" {
		if _, err := os.Stat(override); err != nil {
			return "", fmt.Errorf("an architecture delta is required, but NM_CHROMIUM is not a browser: %w", err)
		}
		return override, nil
	}
	var candidates []string
	for _, cache := range []string{filepath.Join(home, "Library", "Caches", "ms-playwright"), filepath.Join(home, ".cache", "ms-playwright")} {
		shells, _ := filepath.Glob(filepath.Join(cache, "chromium_headless_shell-*", "*", "chrome-headless-shell"))
		sort.Sort(sort.Reverse(sort.StringSlice(shells)))
		candidates = append(candidates, shells...)
	}
	for _, name := range []string{"chrome-headless-shell", "chromium", "chromium-browser", "google-chrome", "google-chrome-stable"} {
		if p, err := lookPath(name); err == nil {
			candidates = append(candidates, p)
		}
	}
	candidates = append(candidates,
		"/Applications/Chromium.app/Contents/MacOS/Chromium",
		"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
	)
	for _, p := range candidates {
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return p, nil
		}
	}
	return "", errors.New("an architecture delta is required, but no Chromium is installed to photograph it; install one with `npx playwright install chromium-headless-shell`, or set NM_CHROMIUM to a Chromium or Chrome binary")
}

func (d architectureDeltaDeps) runShoot(ctx context.Context, browser, page, png string) error {
	if d.shoot != nil {
		return d.shoot(ctx, browser, page, png)
	}
	profile, err := os.MkdirTemp("", "nm-delta-browser-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(profile)
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, browser,
		"--headless=new", "--disable-gpu", "--hide-scrollbars", "--no-first-run", "--no-default-browser-check",
		"--user-data-dir="+profile,
		"--host-resolver-rules=MAP * ~NOTFOUND",
		fmt.Sprintf("--force-device-scale-factor=%d", architectureDeltaScale),
		fmt.Sprintf("--window-size=%d,%d", architectureDeltaWidth, architectureDeltaHeight),
		"--screenshot="+png,
		(&url.URL{Scheme: "file", Path: page}).String(),
	)
	winproc.Harden(cmd)
	shellenv.ConfigureShellCommand(cmd)
	out, err := shellenv.CombinedOutputShellCommand(cmd)
	if _, statErr := os.Stat(png); err != nil || statErr != nil {
		return fmt.Errorf("the browser wrote no screenshot: %v: %s", err, lastLines(string(out), 3))
	}
	return nil
}

// lastLines keeps the end of a browser's chatter for an error.
func lastLines(text string, n int) string {
	lines := nonEmptyLines(strings.TrimSpace(text))
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
