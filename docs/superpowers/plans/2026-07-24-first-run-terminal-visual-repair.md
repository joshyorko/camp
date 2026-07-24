# First-Run Terminal Visual Repair Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the disconnected, literal-space, height-blind "Trailhead Topography" renderer with one measured, height-aware, cohesively composed full-screen campsite scene — used identically by setup prompts, in-progress waypoints, the ready state, and failures — without changing any lifecycle, capability-gating, or JSON contract.

**Architecture:** Add a small internal measured-layout toolkit (`internal/presentation/layout.go`) that computes visible width (stripping ANSI) and centers/aligns/columns/vertically-distributes content instead of hand-typed literal-space strings. Add an explicit 4-state waypoint model (`pending/active/completed/failed`). Build one scene composer (`internal/presentation/scene.go`) that both the static campsite render and the setup animator delegate to, so every stage (prompts → waypoints → ready/failure) shares the same backdrop and never feels like a separate fragment. Thread terminal height from the OS probe through to the composer. Integrate the pre-init configuration prompts into the same full-screen scene when color is available, while keeping the plain/JSON paths byte-identical to today.

**Tech Stack:** Go 1.25, `golang.org/x/sys/unix` (ioctl winsize), Cobra, no new third-party dependency (a bespoke layout toolkit is intentionally hand-rolled — the codebase already avoids TUI frameworks and the primitives needed are small).

## Global Constraints

- Never invent a percentage, timer, sleep, or time-derived readiness claim (`docs/skills/terminal-experience.md:3,15`).
- `CAMP IS READY` may appear only in the storage frame / after storage is verified (`docs/skills/terminal-experience.md:15`).
- Human setup output never prints managed executable paths, checksums, or a `PATH` export (`docs/superpowers/specs/2026-07-23-setup-first-run-experience-design.md:23`).
- JSON output never prompts and stays stable/detailed (`docs/superpowers/specs/2026-07-23-setup-first-run-experience-design.md:19,23`).
- Full-color output requires TTY, width ≥ 80, `COLORTERM` truecolor/24bit, non-dumb `TERM`, no `CI`/`NO_COLOR`/JSON; everything else is deterministic control-free plain text (`docs/skills/terminal-experience.md:9`).
- EOF or an empty required prompt value fails before `init` is called; no partial configuration is ever persisted (`docs/skills/terminal-experience.md:13`).
- Presentation values are rejected (control chars, credential-bearing URLs) before any byte is written (`docs/skills/terminal-experience.md:7`, `internal/presentation/campsite.go:46-97` `validateCampsiteModel`/`unsafeCampsiteValue` — unchanged).
- Failures preserve the real underlying error message and print at most one exact recovery command (`docs/skills/terminal-experience.md:17`).
- Golden coverage must include wide true-color, plain/narrow, plain failure, and cancellation-with-no-output paths, plus the capability matrix (`docs/skills/terminal-experience.md:33`) — all of this must keep passing.
- No mouse requirement; keyboard-only; the OS text cursor (never manually positioned) is the only focus indicator needed.

---

### Task 1: Terminal height plumbing

**Files:**
- Modify: `internal/presentation/campsite.go:29-32` (`CampsiteOptions`)
- Modify: `internal/cli/terminal_linux.go:16,18-38` (`terminalProbe`, `resolveTerminalExperience`, `probeTerminal`, `writeHumanLifecycleResult`)
- Modify: `internal/cli/production_setup.go:82-120` (`Setup`)
- Modify: `internal/cli/campsite.go:1-56` (`renderProductionSetupCampsite`)
- Test: `internal/cli/terminal_test.go:12-53` (existing `resolveTerminalExperience` tests) + new height-flow test

**Interfaces:**
- Consumes: `golang.org/x/sys/unix.IoctlGetWinsize` (already used).
- Produces: `presentation.ScreenSize{Width, Height int}`; `resolveTerminalExperience(mode OutputMode, out io.Writer, environment map[string]string, probe terminalProbe) (presentation.TerminalExperience, int, int)`; `type terminalProbe func(uintptr) (bool, int, int)`; `renderProductionSetupCampsite(ctx, out, lockBytes, experience presentation.TerminalExperience, width, height int) error`.

- [ ] **Step 1: Write failing tests**

Add to `internal/presentation/campsite.go` — no test needed for the bare struct field, but add this to `internal/presentation/campsite_test.go` to lock the shape in:

```go
func TestScreenSizeFieldsAreIndependentOfColorOptions(t *testing.T) {
	options := CampsiteOptions{Color: true, Width: 120, Height: 40}
	if options.Width != 120 || options.Height != 40 {
		t.Fatalf("options = %#v", options)
	}
}
```

Update `internal/cli/terminal_test.go` — change the two existing tests' inline probe closures from `func(uintptr) (bool, int)` to `func(uintptr) (bool, int, int)`, change the call-site assertion from `got := resolveTerminalExperience(...)` to `got, _, _ := resolveTerminalExperience(...)`, and add:

```go
func TestResolveTerminalExperienceReturnsProbedHeightAlongsideWidth(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "terminal")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	probe := func(uintptr) (bool, int, int) { return true, 120, 40 }
	experience, width, height := resolveTerminalExperience(ModeHuman, file, map[string]string{"TERM": "xterm-256color", "COLORTERM": "truecolor"}, probe)
	if experience != presentation.TerminalColor || width != 120 || height != 40 {
		t.Fatalf("resolveTerminalExperience() = %q %d %d, want color 120 40", experience, width, height)
	}
}
```

- [ ] **Step 2: Verify RED**

Run: `go build ./... && go test ./internal/cli -run TestResolveTerminalExperience -count=1`

Expected: FAIL — `probeTerminal`/`resolveTerminalExperience` still return two values, so the new/updated tests won't compile, and `go build` fails because `renderProductionSetupCampsite`/`Setup` callers haven't been updated yet (this step surfaces as a compile error, which is the correct RED for a signature change).

- [ ] **Step 3: Implement the minimal plumbing**

In `internal/presentation/campsite.go`, change:

```go
type CampsiteOptions struct {
	Color bool
	Width int
}
```

to:

```go
type ScreenSize struct {
	Width  int
	Height int
}

type CampsiteOptions struct {
	Color  bool
	Width  int
	Height int
}
```

In `internal/cli/terminal_linux.go`, replace:

```go
type terminalProbe func(uintptr) (bool, int)

func resolveTerminalExperience(mode OutputMode, out io.Writer, environment map[string]string, probe terminalProbe) presentation.TerminalExperience {
	file, ok := out.(*os.File)
	if !ok {
		return presentation.TerminalPlain
	}
	tty, width := probe(file.Fd())
	_, noColor := environment["NO_COLOR"]
	ci := strings.TrimSpace(environment["CI"])
	return presentation.SelectTerminalExperience(presentation.TerminalInput{
		TTY: tty, Width: width, TERM: environment["TERM"], COLORTERM: environment["COLORTERM"],
		JSON: mode == ModeJSON, NoColor: noColor, CI: ci != "" && !strings.EqualFold(ci, "false") && ci != "0",
	})
}

func probeTerminal(fd uintptr) (bool, int) {
	winsize, err := unix.IoctlGetWinsize(int(fd), unix.TIOCGWINSZ)
	if err != nil || winsize.Col == 0 {
		return false, 0
	}
	return true, int(winsize.Col)
}
```

with:

```go
type terminalProbe func(uintptr) (bool, int, int)

func resolveTerminalExperience(mode OutputMode, out io.Writer, environment map[string]string, probe terminalProbe) (presentation.TerminalExperience, int, int) {
	file, ok := out.(*os.File)
	if !ok {
		return presentation.TerminalPlain, 0, 0
	}
	tty, width, height := probe(file.Fd())
	_, noColor := environment["NO_COLOR"]
	ci := strings.TrimSpace(environment["CI"])
	experience := presentation.SelectTerminalExperience(presentation.TerminalInput{
		TTY: tty, Width: width, TERM: environment["TERM"], COLORTERM: environment["COLORTERM"],
		JSON: mode == ModeJSON, NoColor: noColor, CI: ci != "" && !strings.EqualFold(ci, "false") && ci != "0",
	})
	return experience, width, height
}

func probeTerminal(fd uintptr) (bool, int, int) {
	winsize, err := unix.IoctlGetWinsize(int(fd), unix.TIOCGWINSZ)
	if err != nil || winsize.Col == 0 {
		return false, 0, 0
	}
	return true, int(winsize.Col), int(winsize.Row)
}
```

And update its only other caller in the same file:

```go
func writeHumanLifecycleResult(out io.Writer, mode OutputMode, operation string, events []presentation.LifecycleEvent, legacy string) error {
	if mode != ModeHuman {
		return nil
	}
	experience, _, _ := resolveTerminalExperience(mode, out, environmentMap(os.Environ()), probeTerminal)
	if len(events) == 0 {
		_, err := io.WriteString(out, legacy)
		return err
	}
	return writeLifecycleEvents(out, experience, operation, events...)
}
```

In `internal/cli/production_setup.go`, in `Setup`, replace the line `experience := resolveTerminalExperience(mode, out, environment, probeTerminal)` with `experience, width, height := resolveTerminalExperience(mode, out, environment, probeTerminal)` (move this line to immediately after the `paths, err := config.ResolveXDGPaths(...)` block, before the `mode == ModeHuman` prompt block, since Task 7 will need `experience`/`width`/`height` there too), and change the final line from `return renderProductionSetupCampsite(ctx, out, lockBytes)` to `return renderProductionSetupCampsite(ctx, out, lockBytes, experience, width, height)`.

In `internal/cli/campsite.go`, remove the `"os"` import (it becomes unused) and change:

```go
func renderProductionSetupCampsite(ctx context.Context, out io.Writer, lockBytes []byte) error {
	lock, err := tooladapter.ParseLock(bytes.NewReader(lockBytes))
	if err != nil {
		return err
	}
	settings, err := resolveProductionSettings()
	if err != nil {
		return err
	}
	experience := resolveTerminalExperience(ModeHuman, out, environmentMap(os.Environ()), probeTerminal)
	if settings.runtime.Source == "" {
```

to:

```go
func renderProductionSetupCampsite(ctx context.Context, out io.Writer, lockBytes []byte, experience presentation.TerminalExperience, width, height int) error {
	lock, err := tooladapter.ParseLock(bytes.NewReader(lockBytes))
	if err != nil {
		return err
	}
	settings, err := resolveProductionSettings()
	if err != nil {
		return err
	}
	if settings.runtime.Source == "" {
```

leaving the rest of the function body unchanged for this task (the `width`/`height` parameters are threaded but not yet consumed — Task 5 wires them into `NewSetupAnimator`).

- [ ] **Step 4: Verify GREEN**

Run: `go build ./... && go test ./internal/presentation ./internal/cli -run 'TestScreenSize|TestResolveTerminalExperience' -count=1`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/presentation/campsite.go internal/presentation/campsite_test.go internal/cli/terminal_linux.go internal/cli/terminal_test.go internal/cli/production_setup.go internal/cli/campsite.go
git commit -m "feat: thread terminal height through setup rendering plumbing"
```

---

### Task 2: Measured layout primitives

**Files:**
- Create: `internal/presentation/layout.go`
- Test: `internal/presentation/layout_test.go`

**Interfaces:**
- Consumes: nothing beyond `regexp`/`strings`/`unicode/utf8`.
- Produces: `visibleWidth(s string) int`, `padRight(s string, width int) string`, `centerLine(s string, width int) string`, `horizontalRule(width int, glyph string) string`, `evenlySpaced(width, count int) []int`, `overlayGlyphs(width int, fill string, glyphs map[int]string) string`, `columnBlock(columns [][]string, gap int) []string`, `columnBlockWidth(columns [][]string, gap int) int`, `distributeVertically(lines []string, height int) []string`, `indentBlock(lines []string, margin int) []string`, `sceneContentWidth(terminalWidth, max int) int`. These are consumed by Task 4's scene composer.

- [ ] **Step 1: Write the failing tests**

Create `internal/presentation/layout_test.go`:

```go
package presentation

import "testing"

func TestVisibleWidthIgnoresANSIEscapes(t *testing.T) {
	colored := "\x1b[38;2;255;171;45mREADY\x1b[0m"
	if got := visibleWidth(colored); got != 5 {
		t.Fatalf("visibleWidth(%q) = %d, want 5", colored, got)
	}
}

func TestCenterLinePadsOnlyTheLeftSide(t *testing.T) {
	got := centerLine("CAMP", 10)
	if got != "   CAMP" {
		t.Fatalf("centerLine = %q, want %q", got, "   CAMP")
	}
}

func TestCenterLineReturnsUnchangedWhenTooWide(t *testing.T) {
	if got := centerLine("TOOLCHAIN", 4); got != "TOOLCHAIN" {
		t.Fatalf("centerLine = %q, want unchanged", got)
	}
}

func TestHorizontalRuleRepeatsGlyphToWidth(t *testing.T) {
	if got := horizontalRule(5, "─"); got != "─────" {
		t.Fatalf("horizontalRule = %q", got)
	}
	if got := horizontalRule(0, "─"); got != "" {
		t.Fatalf("horizontalRule(0) = %q, want empty", got)
	}
}

func TestEvenlySpacedReturnsCenteredOffsetsWithinWidth(t *testing.T) {
	got := evenlySpaced(20, 4)
	want := []int{2, 7, 12, 17}
	if len(got) != len(want) {
		t.Fatalf("evenlySpaced = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("evenlySpaced = %v, want %v", got, want)
		}
	}
}

func TestOverlayGlyphsPlacesMarkersAtIndices(t *testing.T) {
	got := overlayGlyphs(6, "-", map[int]string{0: "◆", 5: "◆"})
	if got != "◆----◆" {
		t.Fatalf("overlayGlyphs = %q", got)
	}
}

func TestColumnBlockAlignsColumnsAndTrimsTrailingSpace(t *testing.T) {
	columns := [][]string{{"A", "alpha"}, {"BB", "b"}}
	got := columnBlock(columns, 2)
	want := []string{"A      BB", "alpha  b"}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("columnBlock = %#v, want %#v", got, want)
	}
	for _, line := range got {
		if line != "" && line[len(line)-1] == ' ' {
			t.Fatalf("columnBlock left trailing whitespace: %q", line)
		}
	}
}

func TestDistributeVerticallyAddsTopAndBottomMarginsWithoutTrailingWhitespace(t *testing.T) {
	got := distributeVertically([]string{"A", "B"}, 8)
	if len(got) != 8 {
		t.Fatalf("len = %d, want 8", len(got))
	}
	if got[2] != "A" || got[3] != "B" {
		t.Fatalf("content misplaced: %#v", got)
	}
	for i, line := range got {
		if i != 2 && i != 3 && line != "" {
			t.Fatalf("row %d = %q, want blank filler", i, line)
		}
	}
}

func TestDistributeVerticallyIsANoOpWhenContentAlreadyFillsHeight(t *testing.T) {
	lines := []string{"A", "B", "C"}
	got := distributeVertically(lines, 2)
	if len(got) != 3 {
		t.Fatalf("distributeVertically shrank content: %#v", got)
	}
}

func TestIndentBlockPrefixesNonBlankLinesOnlyAndLeavesNoTrailingWhitespace(t *testing.T) {
	got := indentBlock([]string{"", "CAMP"}, 3)
	if got[0] != "" {
		t.Fatalf("blank line must stay blank, got %q", got[0])
	}
	if got[1] != "   CAMP" {
		t.Fatalf("indent = %q, want %q", got[1], "   CAMP")
	}
}

func TestSceneContentWidthCapsToMaxAndRespectsNarrowTerminals(t *testing.T) {
	if got := sceneContentWidth(160, 96); got != 96 {
		t.Fatalf("sceneContentWidth(160) = %d, want 96", got)
	}
	if got := sceneContentWidth(80, 96); got != 76 {
		t.Fatalf("sceneContentWidth(80) = %d, want 76", got)
	}
}
```

- [ ] **Step 2: Verify RED**

Run: `go test ./internal/presentation -run 'TestVisibleWidth|TestCenterLine|TestHorizontalRule|TestEvenlySpaced|TestOverlayGlyphs|TestColumnBlock|TestDistributeVertically|TestIndentBlock|TestSceneContentWidth' -count=1`

Expected: FAIL — `layout.go` does not exist yet, so this fails to compile.

- [ ] **Step 3: Implement the minimal layout primitives**

Create `internal/presentation/layout.go`:

```go
package presentation

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

var ansiEscapePattern = regexp.MustCompile("\x1b\\[[0-9;]*m")

func visibleWidth(s string) int {
	return utf8.RuneCountInString(ansiEscapePattern.ReplaceAllString(s, ""))
}

func padRight(s string, width int) string {
	if pad := width - visibleWidth(s); pad > 0 {
		return s + strings.Repeat(" ", pad)
	}
	return s
}

func centerLine(s string, width int) string {
	pad := width - visibleWidth(s)
	if pad <= 0 {
		return s
	}
	return strings.Repeat(" ", pad/2) + s
}

func horizontalRule(width int, glyph string) string {
	if width <= 0 {
		return ""
	}
	return strings.Repeat(glyph, width)
}

func evenlySpaced(width, count int) []int {
	if count <= 0 || width <= 0 {
		return nil
	}
	step := width / count
	offset := step / 2
	positions := make([]int, count)
	for i := 0; i < count; i++ {
		position := offset + i*step
		if position >= width {
			position = width - 1
		}
		positions[i] = position
	}
	return positions
}

func overlayGlyphs(width int, fill string, glyphs map[int]string) string {
	cells := make([]string, width)
	for i := range cells {
		cells[i] = fill
	}
	for index, glyph := range glyphs {
		if index >= 0 && index < width {
			cells[index] = glyph
		}
	}
	return strings.Join(cells, "")
}

func columnBlock(columns [][]string, gap int) []string {
	height := 0
	widths := make([]int, len(columns))
	for i, column := range columns {
		if len(column) > height {
			height = len(column)
		}
		for _, line := range column {
			if w := visibleWidth(line); w > widths[i] {
				widths[i] = w
			}
		}
	}
	spacer := strings.Repeat(" ", gap)
	rows := make([]string, height)
	for row := 0; row < height; row++ {
		var line strings.Builder
		for i, column := range columns {
			if i > 0 {
				line.WriteString(spacer)
			}
			cell := ""
			if row < len(column) {
				cell = column[row]
			}
			line.WriteString(padRight(cell, widths[i]))
		}
		rows[row] = strings.TrimRight(line.String(), " ")
	}
	return rows
}

func columnBlockWidth(columns [][]string, gap int) int {
	total := 0
	for i, column := range columns {
		width := 0
		for _, line := range column {
			if w := visibleWidth(line); w > width {
				width = w
			}
		}
		total += width
		if i > 0 {
			total += gap
		}
	}
	return total
}

func distributeVertically(lines []string, height int) []string {
	if height <= len(lines) {
		return lines
	}
	extra := height - len(lines)
	top := extra / 3
	bottom := extra - top
	out := make([]string, 0, height)
	for i := 0; i < top; i++ {
		out = append(out, "")
	}
	out = append(out, lines...)
	for i := 0; i < bottom; i++ {
		out = append(out, "")
	}
	return out
}

func indentBlock(lines []string, margin int) []string {
	if margin <= 0 {
		return lines
	}
	prefix := strings.Repeat(" ", margin)
	out := make([]string, len(lines))
	for i, line := range lines {
		if line == "" {
			continue
		}
		out[i] = prefix + line
	}
	return out
}

func sceneContentWidth(terminalWidth, max int) int {
	width := terminalWidth - 4
	if width > max {
		return max
	}
	if width < 1 {
		return terminalWidth
	}
	return width
}
```

- [ ] **Step 4: Verify GREEN**

Run: `go test ./internal/presentation -run 'TestVisibleWidth|TestCenterLine|TestHorizontalRule|TestEvenlySpaced|TestOverlayGlyphs|TestColumnBlock|TestDistributeVertically|TestIndentBlock|TestSceneContentWidth' -count=1 -v`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/presentation/layout.go internal/presentation/layout_test.go
git commit -m "feat: add measured layout primitives for terminal scene composition"
```

---

### Task 3: Four-state waypoint model

**Files:**
- Create: `internal/presentation/waypoint.go`
- Test: `internal/presentation/waypoint_test.go`

**Interfaces:**
- Consumes: nothing new.
- Produces: palette constants (`colorReset`, `colorSky`, `colorPine`, `colorMeadow`, `colorCanvas`, `colorAmber`, `colorGreen`, `colorBlue`, `colorRed`, `colorDim`), `type WaypointStatus int` with `WaypointPending/WaypointActive/WaypointCompleted/WaypointFailed`, `(WaypointStatus) glyph() string`, `(WaypointStatus) color() string`, `(WaypointStatus) paint(label string) string`, `waypointStatuses(completed, failedAt int) [4]WaypointStatus`. Consumed by Task 4's scene composer and Task 5's animator wiring.

- [ ] **Step 1: Write the failing tests**

Create `internal/presentation/waypoint_test.go`:

```go
package presentation

import "testing"

func TestWaypointStatusesMarksCompletedActiveAndPending(t *testing.T) {
	got := waypointStatuses(2, -1)
	want := [4]WaypointStatus{WaypointCompleted, WaypointCompleted, WaypointActive, WaypointPending}
	if got != want {
		t.Fatalf("waypointStatuses(2, -1) = %v, want %v", got, want)
	}
}

func TestWaypointStatusesAllCompletedWhenFullyDone(t *testing.T) {
	got := waypointStatuses(4, -1)
	want := [4]WaypointStatus{WaypointCompleted, WaypointCompleted, WaypointCompleted, WaypointCompleted}
	if got != want {
		t.Fatalf("waypointStatuses(4, -1) = %v, want %v", got, want)
	}
}

func TestWaypointStatusesMarksFailureAndLeavesLaterWaypointsPending(t *testing.T) {
	got := waypointStatuses(1, 1)
	want := [4]WaypointStatus{WaypointCompleted, WaypointFailed, WaypointPending, WaypointPending}
	if got != want {
		t.Fatalf("waypointStatuses(1, 1) = %v, want %v", got, want)
	}
}

func TestWaypointStatusGlyphsAreDistinctPerState(t *testing.T) {
	seen := map[string]bool{}
	for _, status := range []WaypointStatus{WaypointPending, WaypointActive, WaypointCompleted, WaypointFailed} {
		glyph := status.glyph()
		if seen[glyph] {
			t.Fatalf("glyph %q reused across states", glyph)
		}
		seen[glyph] = true
	}
}

func TestWaypointStatusColorsAreDistinctPerState(t *testing.T) {
	seen := map[string]bool{}
	for _, status := range []WaypointStatus{WaypointPending, WaypointActive, WaypointCompleted, WaypointFailed} {
		color := status.color()
		if seen[color] {
			t.Fatalf("color %q reused across states", color)
		}
		seen[color] = true
	}
}

func TestWaypointStatusPaintWrapsGlyphLabelAndReset(t *testing.T) {
	got := WaypointCompleted.paint("TOOLCHAIN")
	want := colorGreen + "✓ TOOLCHAIN" + colorReset
	if got != want {
		t.Fatalf("paint = %q, want %q", got, want)
	}
}
```

- [ ] **Step 2: Verify RED**

Run: `go test ./internal/presentation -run TestWaypointStatus -count=1`

Expected: FAIL — `waypoint.go` does not exist.

- [ ] **Step 3: Implement the minimal waypoint model**

Create `internal/presentation/waypoint.go`:

```go
package presentation

const (
	colorReset  = "\x1b[0m"
	colorSky    = "\x1b[38;2;108;178;235m"
	colorPine   = "\x1b[38;2;58;110;70m"
	colorMeadow = "\x1b[38;2;140;188;92m"
	colorCanvas = "\x1b[38;2;238;215;169m"
	colorAmber  = "\x1b[38;2;255;171;45m"
	colorGreen  = "\x1b[38;2;102;214;86m"
	colorBlue   = "\x1b[38;2;56;155;255m"
	colorRed    = "\x1b[38;2;233;77;77m"
	colorDim    = "\x1b[38;2;110;118;129m"
)

type WaypointStatus int

const (
	WaypointPending WaypointStatus = iota
	WaypointActive
	WaypointCompleted
	WaypointFailed
)

func (status WaypointStatus) glyph() string {
	switch status {
	case WaypointCompleted:
		return "✓"
	case WaypointActive:
		return "◐"
	case WaypointFailed:
		return "✗"
	default:
		return "○"
	}
}

func (status WaypointStatus) color() string {
	switch status {
	case WaypointCompleted:
		return colorGreen
	case WaypointActive:
		return colorAmber
	case WaypointFailed:
		return colorRed
	default:
		return colorDim
	}
}

func (status WaypointStatus) paint(label string) string {
	return status.color() + status.glyph() + " " + label + colorReset
}

func waypointStatuses(completed, failedAt int) [4]WaypointStatus {
	var statuses [4]WaypointStatus
	for i := range statuses {
		switch {
		case failedAt >= 0 && i == failedAt:
			statuses[i] = WaypointFailed
		case failedAt >= 0 && i > failedAt:
			statuses[i] = WaypointPending
		case i < completed:
			statuses[i] = WaypointCompleted
		case i == completed:
			statuses[i] = WaypointActive
		default:
			statuses[i] = WaypointPending
		}
	}
	return statuses
}
```

- [ ] **Step 4: Verify GREEN**

Run: `go test ./internal/presentation -run TestWaypointStatus -count=1 -v`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/presentation/waypoint.go internal/presentation/waypoint_test.go
git commit -m "feat: model pending/active/completed/failed waypoint states"
```

---

### Task 4: Trailhead Topography scene composer

**Files:**
- Create: `internal/presentation/scene.go`
- Test: `internal/presentation/scene_test.go`

**Interfaces:**
- Consumes: `layout.go` primitives (Task 2), `waypoint.go` model (Task 3), `CampsiteModel` (existing, `internal/presentation/campsite.go:16-27`), `ScreenSize` (Task 1).
- Produces: `type sceneFailure struct { Waypoint SetupWaypoint; Message, Recovery string }`, `composeScene(model CampsiteModel, statuses [4]WaypointStatus, size ScreenSize, ready bool, failure *sceneFailure) string`. Consumed by Task 5 (`renderColorCampsite`, `renderSetupColorFrame`, `renderSetupFailureFrame`).

- [ ] **Step 1: Write the failing tests**

Create `internal/presentation/scene_test.go`:

```go
package presentation

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func testSceneModel() CampsiteModel {
	return CampsiteModel{
		DevPod: ToolIdentity{Name: "DevPod", Version: "v0.26.1"}, Hauler: ToolIdentity{Name: "Hauler", Version: "v2.0.2"},
		Provider: "docker", RuntimeKind: "local DevPod", Context: "default", Capsule: "second_brain", Source: "/home/josh/second_brain",
		BackendKind: "file", Storage: "no committed generation", NextCommand: "camp open second_brain",
	}
}

func TestComposeSceneShowsReadyBandOnlyWhenAllWaypointsCompleted(t *testing.T) {
	model := testSceneModel()
	inProgress := composeScene(model, waypointStatuses(2, -1), ScreenSize{Width: 120, Height: 40}, false, nil)
	if strings.Contains(inProgress, "CAMP IS READY") {
		t.Fatalf("in-progress frame must not claim readiness:\n%s", inProgress)
	}
	ready := composeScene(model, waypointStatuses(4, -1), ScreenSize{Width: 120, Height: 40}, true, nil)
	if !strings.Contains(ready, "CAMP IS READY") {
		t.Fatalf("ready frame must show CAMP IS READY:\n%s", ready)
	}
	if !strings.Contains(ready, "camp open second_brain") {
		t.Fatalf("ready frame must show the next command:\n%s", ready)
	}
}

func TestComposeSceneCarriesRealMetadataPerWaypoint(t *testing.T) {
	model := testSceneModel()
	got := composeScene(model, waypointStatuses(4, -1), ScreenSize{Width: 120, Height: 40}, true, nil)
	for _, want := range []string{
		"DevPod v0.26.1", "Hauler v2.0.2",
		"docker · local DevPod", "context default",
		"second_brain", "/home/josh/second_brain",
		"file backend", "no committed generation",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("scene missing real metadata %q:\n%s", want, got)
		}
	}
}

func TestComposeSceneFailurePreservesExactCauseAndRecovery(t *testing.T) {
	model := testSceneModel()
	failure := &sceneFailure{Waypoint: SetupRuntime, Message: "devpod provider docker is unreachable", Recovery: "camp setup"}
	got := composeScene(model, waypointStatuses(1, 1), ScreenSize{Width: 120, Height: 40}, false, failure)
	if !strings.Contains(got, "devpod provider docker is unreachable") {
		t.Fatalf("failure frame lost the real cause:\n%s", got)
	}
	if !strings.Contains(got, "camp setup") {
		t.Fatalf("failure frame lost the recovery command:\n%s", got)
	}
	if strings.Contains(got, "CAMP IS READY") {
		t.Fatalf("failure frame must never claim readiness:\n%s", got)
	}
	if strings.Count(got, "camp setup") != 1 {
		t.Fatalf("failure frame must print exactly one recovery command:\n%s", got)
	}
}

func TestComposeSceneNeverExceedsRequestedWidthOrHeight(t *testing.T) {
	sizes := []ScreenSize{{Width: 80, Height: 24}, {Width: 100, Height: 30}, {Width: 120, Height: 40}, {Width: 160, Height: 48}}
	model := testSceneModel()
	for _, size := range sizes {
		got := composeScene(model, waypointStatuses(4, -1), size, true, nil)
		lines := strings.Split(strings.TrimSuffix(got, "\n"), "\n")
		if len(lines) > size.Height {
			t.Fatalf("size %+v: rendered %d lines, exceeds height %d", size, len(lines), size.Height)
		}
		for _, line := range lines {
			visible := ansiEscapePattern.ReplaceAllString(line, "")
			if width := utf8.RuneCountInString(visible); width > size.Width {
				t.Fatalf("size %+v: line %q has visible width %d, exceeds terminal width", size, visible, width)
			}
		}
	}
}

func TestComposeSceneHasNoTrailingWhitespacePerLine(t *testing.T) {
	model := testSceneModel()
	got := composeScene(model, waypointStatuses(4, -1), ScreenSize{Width: 120, Height: 40}, true, nil)
	for _, line := range strings.Split(strings.TrimSuffix(got, "\n"), "\n") {
		if line != "" && strings.HasSuffix(line, " ") {
			t.Fatalf("line has trailing whitespace: %q", line)
		}
	}
}
```

- [ ] **Step 2: Verify RED**

Run: `go test ./internal/presentation -run TestComposeScene -count=1`

Expected: FAIL — `scene.go` does not exist.

- [ ] **Step 3: Implement the minimal scene composer**

Create `internal/presentation/scene.go`:

```go
package presentation

import "fmt"

const sceneMaxWidth = 96
const compactHeightThreshold = 20

type sceneFailure struct {
	Waypoint SetupWaypoint
	Message  string
	Recovery string
}

func composeScene(model CampsiteModel, statuses [4]WaypointStatus, size ScreenSize, ready bool, failure *sceneFailure) string {
	width := size.Width
	if width < 80 {
		width = 80
	}
	contentWidth := sceneContentWidth(width, sceneMaxWidth)
	margin := (width - contentWidth) / 2
	compact := size.Height > 0 && size.Height < compactHeightThreshold

	var lines []string
	lines = append(lines, "")
	lines = append(lines, centerLine(colorCanvas+"⛺ CAMP"+colorReset, contentWidth))
	lines = append(lines, centerLine(colorDim+"trailhead setup"+colorReset, contentWidth))
	lines = append(lines, "")
	if !compact {
		lines = append(lines, skyRow(contentWidth))
		lines = append(lines, topographyRow(contentWidth))
		lines = append(lines, "")
	}
	lines = append(lines, waypointTable(model, statuses, contentWidth)...)
	lines = append(lines, "")
	lines = append(lines, routeRule(statuses, contentWidth))
	lines = append(lines, "")
	switch {
	case failure != nil:
		lines = append(lines, failureBand(*failure, contentWidth)...)
	case ready:
		lines = append(lines, readyBand(model.NextCommand, contentWidth)...)
	}

	if size.Height > 0 {
		lines = distributeVertically(lines, size.Height)
	}
	lines = indentBlock(lines, margin)

	out := ""
	for _, line := range lines {
		out += line + "\n"
	}
	return out
}

func skyRow(width int) string {
	glyphs := map[int]string{}
	for i, index := range evenlySpaced(width, 5) {
		glyph := "·"
		if i%2 == 1 {
			glyph = "✦"
		}
		glyphs[index] = glyph
	}
	return colorSky + overlayGlyphs(width, " ", glyphs) + colorReset
}

func topographyRow(width int) string {
	peaks := evenlySpaced(width, 5)
	tentIndex := peaks[len(peaks)/2]
	glyphs := map[int]string{}
	for _, index := range peaks {
		glyphs[index] = "▲"
	}
	line := overlayGlyphs(width, " ", glyphs)
	out := ""
	for i, char := range []rune(line) {
		switch {
		case i == tentIndex:
			out += colorCanvas + "▲" + colorReset
		case char == '▲':
			out += colorPine + "▲" + colorReset
		default:
			out += " "
		}
	}
	return out
}

func waypointTable(model CampsiteModel, statuses [4]WaypointStatus, width int) []string {
	labels := []string{"TOOLCHAIN", "RUNTIME", "CAPSULE", "STORAGE"}
	metadata := [][]string{
		{fmt.Sprintf("%s %s", model.DevPod.Name, model.DevPod.Version), fmt.Sprintf("%s %s", model.Hauler.Name, model.Hauler.Version)},
		{fmt.Sprintf("%s · %s", model.Provider, model.RuntimeKind), "context " + model.Context},
		{model.Capsule, model.Source},
		{model.BackendKind + " backend", model.Storage},
	}
	columns := make([][]string, len(labels))
	for i, label := range labels {
		column := []string{statuses[i].paint(label)}
		for _, line := range metadata[i] {
			column = append(column, colorCanvas+line+colorReset)
		}
		columns[i] = column
	}
	const gap = 3
	rows := columnBlock(columns, gap)
	if tableWidth := columnBlockWidth(columns, gap); tableWidth < width {
		rows = indentBlock(rows, (width-tableWidth)/2)
	}
	return rows
}

func routeRule(statuses [4]WaypointStatus, width int) string {
	glyphs := map[int]string{}
	for i, index := range evenlySpaced(width, len(statuses)) {
		glyphs[index] = statuses[i].glyph()
	}
	line := overlayGlyphs(width, "─", glyphs)
	switch {
	case containsStatus(statuses, WaypointFailed):
		return colorRed + line + colorReset
	case statuses[len(statuses)-1] == WaypointCompleted:
		return colorAmber + line + "🔥" + colorReset
	default:
		return colorDim + line + colorReset
	}
}

func containsStatus(statuses [4]WaypointStatus, target WaypointStatus) bool {
	for _, status := range statuses {
		if status == target {
			return true
		}
	}
	return false
}

func readyBand(nextCommand string, width int) []string {
	return []string{
		centerLine(colorAmber+"CAMP IS READY"+colorReset, width),
		"",
		centerLine(colorBlue+"> "+nextCommand+colorReset, width),
	}
}

func failureBand(failure sceneFailure, width int) []string {
	return []string{
		centerLine(colorRed+"stopped: "+failure.Message+colorReset, width),
		centerLine(colorRed+"next: "+failure.Recovery+colorReset, width),
	}
}
```

- [ ] **Step 4: Verify GREEN**

Run: `go test ./internal/presentation -run TestComposeScene -count=1 -v`

Expected: PASS. If `TestComposeSceneNeverExceedsRequestedWidthOrHeight` fails at `Width: 80` because the sky/topography/title rows push past 24 lines together with the table+route+ready band, reduce decorative content further in the compact branch (e.g. drop the `"trailhead setup"` subtitle line too) until it fits — do not raise `compactHeightThreshold` to "fix" the test, since 24 rows is a required supported size.

- [ ] **Step 5: Commit**

```bash
git add internal/presentation/scene.go internal/presentation/scene_test.go
git commit -m "feat: compose one shared Trailhead Topography scene renderer"
```

---

### Task 5: Wire the scene composer into the campsite and setup animator, add the failure frame

**Files:**
- Modify: `internal/presentation/campsite.go:34-44,115-147` (`RenderCampsite`, `renderColorCampsite`)
- Modify: `internal/presentation/setup.go` (entire file)
- Modify: `internal/presentation/terminal_test.go:106-176` (`NewSetupAnimator` call sites)
- Modify: `internal/cli/campsite.go` (`NewSetupAnimator` call site)
- Test: `internal/presentation/setup_test.go` (new, for `Fail`)

**Interfaces:**
- Consumes: `composeScene` (Task 4), `ScreenSize` (Task 1).
- Produces: `RenderCampsite(writer io.Writer, model CampsiteModel, options CampsiteOptions) error` (signature unchanged), `NewSetupAnimator(writer io.Writer, experience TerminalExperience, model CampsiteModel, size ScreenSize) (*SetupAnimator, error)`, `(*SetupAnimator) Fail(ctx context.Context, waypoint SetupWaypoint, cause error, recovery string) error`.

- [ ] **Step 1: Write the failing tests**

Update `internal/presentation/terminal_test.go`: every call to `NewSetupAnimator(&output, TerminalColor, model)` or `NewSetupAnimator(&output, TerminalPlain, model)` (6 occurrences across `TestSetupAnimatorRedrawsColorSceneOnlyFromOrderedWaypoints`, `TestSetupAnimatorRejectsSkippedOrRepeatedWaypointBeforeWriting`, `TestSetupAnimatorPlainFallbackIsStableAndControlFree`) gains a fourth argument, e.g. `NewSetupAnimator(&output, TerminalColor, model, ScreenSize{Width: 120, Height: 40})` for color tests and `NewSetupAnimator(&output, TerminalPlain, model, ScreenSize{})` for plain tests. The existing assertions (`\x1b[2J\x1b[H` count of 4, `CAMP IS READY` count of 1, authoritative values present, plain output byte-for-byte match, out-of-order rejection) stay as-is — they must keep passing against the new composer's output.

Create `internal/presentation/setup_test.go`:

```go
package presentation

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

func TestSetupAnimatorFailRendersFailureFrameWithRealCauseAndRecovery(t *testing.T) {
	model := CampsiteModel{
		DevPod: ToolIdentity{Name: "DevPod", Version: "v0.26.1"}, Hauler: ToolIdentity{Name: "Hauler", Version: "v2.0.2"},
		Provider: "docker", RuntimeKind: "local DevPod", Context: "default", Capsule: "brain", Source: "/brain",
		BackendKind: "file", Storage: "no committed generation", NextCommand: "camp open /brain",
	}
	var output bytes.Buffer
	animator, err := NewSetupAnimator(&output, TerminalColor, model, ScreenSize{Width: 120, Height: 40})
	if err != nil {
		t.Fatalf("NewSetupAnimator: %v", err)
	}
	if err := animator.Advance(context.Background(), SetupToolchain); err != nil {
		t.Fatalf("Advance: %v", err)
	}
	cause := errors.New("devpod provider docker is unreachable")
	if err := animator.Fail(context.Background(), SetupRuntime, cause, "camp setup"); err != nil {
		t.Fatalf("Fail: %v", err)
	}
	got := output.String()
	if !strings.Contains(got, "devpod provider docker is unreachable") {
		t.Fatalf("output lost the real cause: %q", got)
	}
	if !strings.Contains(got, "camp setup") {
		t.Fatalf("output lost the recovery command: %q", got)
	}
	if strings.Contains(got, "CAMP IS READY") {
		t.Fatalf("failure output must never claim readiness: %q", got)
	}
}

func TestSetupAnimatorFailRejectsOutOfOrderWaypoint(t *testing.T) {
	model := CampsiteModel{
		DevPod: ToolIdentity{Name: "DevPod", Version: "v0.26.1"}, Hauler: ToolIdentity{Name: "Hauler", Version: "v2.0.2"},
		Provider: "docker", RuntimeKind: "local DevPod", Context: "default", Capsule: "brain", Source: "/brain",
		BackendKind: "file", Storage: "no committed generation", NextCommand: "camp open /brain",
	}
	var output bytes.Buffer
	animator, err := NewSetupAnimator(&output, TerminalPlain, model, ScreenSize{})
	if err != nil {
		t.Fatal(err)
	}
	if err := animator.Fail(context.Background(), SetupCapsule, errors.New("boom"), "camp setup"); err == nil {
		t.Fatal("Fail accepted an out-of-order waypoint")
	}
	if output.Len() != 0 {
		t.Fatalf("Fail wrote partial output %q", output.String())
	}
}

func TestSetupAnimatorFailPlainPreservesExistingFailureShape(t *testing.T) {
	model := CampsiteModel{
		DevPod: ToolIdentity{Name: "DevPod", Version: "v0.26.1"}, Hauler: ToolIdentity{Name: "Hauler", Version: "v2.0.2"},
		Provider: "docker", RuntimeKind: "local DevPod", Context: "default", Capsule: "brain", Source: "/brain",
		BackendKind: "file", Storage: "no committed generation", NextCommand: "camp open /brain",
	}
	var output bytes.Buffer
	animator, err := NewSetupAnimator(&output, TerminalPlain, model, ScreenSize{})
	if err != nil {
		t.Fatal(err)
	}
	if err := animator.Fail(context.Background(), SetupToolchain, errors.New("checkpoint upload failed"), "camp recover session-1"); err != nil {
		t.Fatal(err)
	}
	want := "setup: stopped: checkpoint upload failed\nsetup: recover: camp recover session-1\n"
	if got := output.String(); got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}
```

- [ ] **Step 2: Verify RED**

Run: `go test ./internal/presentation -run 'TestSetupAnimator' -count=1`

Expected: FAIL — `NewSetupAnimator` still takes 3 arguments and has no `Fail` method, so this fails to compile.

- [ ] **Step 3: Implement the minimal wiring**

In `internal/presentation/campsite.go`, replace `renderColorCampsite`'s call inside `RenderCampsite` and the function itself:

```go
func RenderCampsite(writer io.Writer, model CampsiteModel, options CampsiteOptions) error {
	if err := validateCampsiteModel(model); err != nil {
		return err
	}
	if options.Color && options.Width >= 80 {
		_, err := io.WriteString(writer, renderColorCampsite(model, ScreenSize{Width: options.Width, Height: options.Height}))
		return err
	}
	_, err := io.WriteString(writer, renderPlainCampsite(model))
	return err
}
```

and delete the old hand-typed `renderColorCampsite` body (`internal/presentation/campsite.go:115-147`), replacing it with:

```go
func renderColorCampsite(model CampsiteModel, size ScreenSize) string {
	return composeScene(model, waypointStatuses(len(setupWaypoints), -1), size, true, nil)
}
```

Replace the whole of `internal/presentation/setup.go` with:

```go
package presentation

import (
	"context"
	"fmt"
	"io"
)

type SetupWaypoint string

const (
	SetupToolchain SetupWaypoint = "toolchain"
	SetupRuntime   SetupWaypoint = "runtime"
	SetupCapsule   SetupWaypoint = "capsule"
	SetupStorage   SetupWaypoint = "storage"
)

var setupWaypoints = []SetupWaypoint{SetupToolchain, SetupRuntime, SetupCapsule, SetupStorage}

type SetupAnimator struct {
	writer     io.Writer
	experience TerminalExperience
	model      CampsiteModel
	size       ScreenSize
	next       int
}

func NewSetupAnimator(writer io.Writer, experience TerminalExperience, model CampsiteModel, size ScreenSize) (*SetupAnimator, error) {
	if writer == nil {
		return nil, fmt.Errorf("setup animator writer is nil")
	}
	if err := validateCampsiteModel(model); err != nil {
		return nil, err
	}
	return &SetupAnimator{writer: writer, experience: experience, model: model, size: size}, nil
}

func (a *SetupAnimator) Advance(ctx context.Context, waypoint SetupWaypoint) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if a.next >= len(setupWaypoints) || waypoint != setupWaypoints[a.next] {
		return fmt.Errorf("setup waypoint %q is out of order", waypoint)
	}
	completed := a.next + 1
	var output string
	if a.experience == TerminalColor {
		statuses := waypointStatuses(completed, -1)
		ready := completed == len(setupWaypoints)
		output = "\x1b[2J\x1b[H" + composeScene(a.model, statuses, a.size, ready, nil)
	} else {
		output = renderSetupPlainEvent(a.model, waypoint)
		if waypoint == SetupStorage {
			output += fmt.Sprintf("setup: camp is ready; next: %s\n", a.model.NextCommand)
		}
	}
	if _, err := io.WriteString(a.writer, output); err != nil {
		return err
	}
	a.next++
	return nil
}

func (a *SetupAnimator) Fail(ctx context.Context, waypoint SetupWaypoint, cause error, recovery string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	index := indexOfWaypoint(waypoint)
	if index == -1 || index != a.next {
		return fmt.Errorf("setup failure waypoint %q is out of order", waypoint)
	}
	message := cause.Error()
	if a.experience == TerminalColor {
		statuses := waypointStatuses(index, index)
		failure := &sceneFailure{Waypoint: waypoint, Message: message, Recovery: recovery}
		_, err := io.WriteString(a.writer, "\x1b[2J\x1b[H"+composeScene(a.model, statuses, a.size, false, failure))
		return err
	}
	_, err := fmt.Fprintf(a.writer, "setup: stopped: %s\nsetup: recover: %s\n", message, recovery)
	return err
}

func indexOfWaypoint(waypoint SetupWaypoint) int {
	for i, candidate := range setupWaypoints {
		if candidate == waypoint {
			return i
		}
	}
	return -1
}

func renderSetupPlainEvent(model CampsiteModel, waypoint SetupWaypoint) string {
	switch waypoint {
	case SetupToolchain:
		return fmt.Sprintf("setup: toolchain: %s %s · %s %s\n", model.DevPod.Name, model.DevPod.Version, model.Hauler.Name, model.Hauler.Version)
	case SetupRuntime:
		return fmt.Sprintf("setup: runtime: %s · %s · context %s\n", model.Provider, model.RuntimeKind, model.Context)
	case SetupCapsule:
		return fmt.Sprintf("setup: capsule: %s · %s\n", model.Capsule, model.Source)
	case SetupStorage:
		return fmt.Sprintf("setup: storage: %s backend · %s\n", model.BackendKind, model.Storage)
	default:
		return ""
	}
}
```

Note this drops the `strings` import entirely (no longer needed) and the old `renderSetupColorFrame` (fully superseded by `composeScene`).

In `internal/cli/campsite.go`, update the animator construction inside `renderProductionSetupCampsite`:

```go
	animator, err := presentation.NewSetupAnimator(out, experience, model, presentation.ScreenSize{Width: width, Height: height})
```

- [ ] **Step 4: Verify GREEN**

Run: `go build ./... && go test ./internal/presentation ./internal/cli -count=1`

Expected: PASS. `TestRenderCampsiteColorKeepsLiveMetadataVisible` (`internal/presentation/campsite_test.go:49-74`) must still pass unmodified — its `Contains` assertions match substrings that `waypointTable`'s metadata lines still produce verbatim.

- [ ] **Step 5: Commit**

```bash
git add internal/presentation/campsite.go internal/presentation/setup.go internal/presentation/terminal_test.go internal/presentation/setup_test.go internal/cli/campsite.go
git commit -m "feat: render one shared scene for waypoints, ready state, and failures"
```

---

### Task 6: Golden coverage across required terminal sizes

**Files:**
- Modify: `internal/presentation/terminal_golden_test.go`
- Modify: `internal/presentation/testdata/campsite-color.golden` (regenerated, not hand-edited)
- Create: `internal/presentation/testdata/setup-scene-80x24.golden`, `setup-scene-100x30.golden`, `setup-scene-120x40.golden`, `setup-scene-160x48.golden`, `setup-scene-130x50.golden`, `setup-scene-failure-120x40.golden` (all captured, not hand-written)

**Interfaces:**
- Consumes: `composeScene`/`NewSetupAnimator`/`RenderCampsite` (Tasks 4–5).
- Produces: a `compareOrUpdateGolden` test helper other presentation tests can reuse; no new production code.

- [ ] **Step 1: Write the failing tests**

In `internal/presentation/terminal_golden_test.go`, add near the top (after imports, which gain `"unicode/utf8"`):

```go
func compareOrUpdateGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if os.Getenv("CAMP_UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("captured golden %s (review before committing)", name)
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Fatalf("golden mismatch for %s\ngot:\n%s\nwant:\n%s", name, got, string(want))
	}
}
```

Refactor the existing `TestTerminalExperienceGoldens` loop body's tail from:

```go
			got := strings.ReplaceAll(output.String(), "\x1b", "<ESC>")
			if got == "" {
				got = "<no output>\n"
			}
			want, err := os.ReadFile(filepath.Join("testdata", test.golden))
			if err != nil {
				t.Fatal(err)
			}
			if got != string(want) {
				t.Fatalf("golden mismatch\ngot:\n%s\nwant:\n%s", got, want)
			}
```

to:

```go
			got := strings.ReplaceAll(output.String(), "\x1b", "<ESC>")
			if got == "" {
				got = "<no output>\n"
			}
			compareOrUpdateGolden(t, test.golden, got)
```

Then add two new tests:

```go
func TestSetupSceneGoldensAcrossSupportedTerminalSizes(t *testing.T) {
	model := CampsiteModel{
		DevPod: ToolIdentity{Name: "DevPod", Version: "v0.26.1"}, Hauler: ToolIdentity{Name: "Hauler", Version: "v2.0.2"},
		Provider: "docker", RuntimeKind: "local DevPod", Context: "default", Capsule: "second_brain", Source: "/home/josh/second_brain",
		BackendKind: "file", Storage: "no committed generation", NextCommand: "camp open second_brain",
	}
	sizes := []struct {
		name          string
		width, height int
	}{
		{"80x24", 80, 24},
		{"100x30", 100, 30},
		{"120x40", 120, 40},
		{"160x48", 160, 48},
		// approximates the terminal geometry behind the reported failure screenshot
		{"130x50", 130, 50},
	}
	for _, size := range sizes {
		t.Run(size.name, func(t *testing.T) {
			var output bytes.Buffer
			animator, err := NewSetupAnimator(&output, TerminalColor, model, ScreenSize{Width: size.width, Height: size.height})
			if err != nil {
				t.Fatal(err)
			}
			for _, waypoint := range []SetupWaypoint{SetupToolchain, SetupRuntime, SetupCapsule, SetupStorage} {
				if err := animator.Advance(context.Background(), waypoint); err != nil {
					t.Fatal(err)
				}
			}
			got := strings.ReplaceAll(output.String(), "\x1b", "<ESC>")
			compareOrUpdateGolden(t, "setup-scene-"+size.name+".golden", got)

			finalFrame := got[strings.LastIndex(got, "<ESC>[2J<ESC>[H"):]
			for _, line := range strings.Split(finalFrame, "\n") {
				visible := ansiEscapePattern.ReplaceAllString(strings.ReplaceAll(line, "<ESC>", "\x1b"), "")
				if width := utf8.RuneCountInString(visible); width > size.width {
					t.Fatalf("%s: line exceeds terminal width %d: %q", size.name, size.width, visible)
				}
			}
			if lines := strings.Count(finalFrame, "\n"); lines > size.height {
				t.Fatalf("%s: final frame rendered %d lines, exceeds terminal height %d", size.name, lines, size.height)
			}
		})
	}
}

func TestSetupAnimatorFailureGoldenAtStandardSize(t *testing.T) {
	model := CampsiteModel{
		DevPod: ToolIdentity{Name: "DevPod", Version: "v0.26.1"}, Hauler: ToolIdentity{Name: "Hauler", Version: "v2.0.2"},
		Provider: "docker", RuntimeKind: "local DevPod", Context: "default", Capsule: "second_brain", Source: "/home/josh/second_brain",
		BackendKind: "file", Storage: "no committed generation", NextCommand: "camp open second_brain",
	}
	var output bytes.Buffer
	animator, err := NewSetupAnimator(&output, TerminalColor, model, ScreenSize{Width: 120, Height: 40})
	if err != nil {
		t.Fatal(err)
	}
	if err := animator.Advance(context.Background(), SetupToolchain); err != nil {
		t.Fatal(err)
	}
	if err := animator.Fail(context.Background(), SetupRuntime, errors.New("devpod provider docker is unreachable"), "camp setup"); err != nil {
		t.Fatal(err)
	}
	got := strings.ReplaceAll(output.String(), "\x1b", "<ESC>")
	compareOrUpdateGolden(t, "setup-scene-failure-120x40.golden", got)
	if !strings.Contains(got, "devpod provider docker is unreachable") || !strings.Contains(got, "camp setup") {
		t.Fatalf("failure golden lost real cause or recovery command: %s", got)
	}
}
```

Add `"errors"` to the import block.

- [ ] **Step 2: Verify RED**

Run: `go test ./internal/presentation -run 'TestSetupSceneGoldensAcrossSupportedTerminalSizes|TestSetupAnimatorFailureGoldenAtStandardSize|TestTerminalExperienceGoldens' -count=1`

Expected: FAIL — the six new `testdata/setup-scene-*.golden` files don't exist yet, and `campsite-color.golden` no longer matches the new composer's output (this is the required witnessed RED proving the old golden encoded the bad layout, not a design authority).

- [ ] **Step 3: Capture and review the new goldens**

Run: `CAMP_UPDATE_GOLDEN=1 go test ./internal/presentation -run 'TestSetupSceneGoldensAcrossSupportedTerminalSizes|TestSetupAnimatorFailureGoldenAtStandardSize|TestTerminalExperienceGoldens' -count=1 -v`

Then open every touched file under `internal/presentation/testdata/` and visually read it (mentally substitute `<ESC>[38;2;R;G;Bm` for color, `<ESC>[2J<ESC>[H` for a full-screen clear) to confirm: no line exceeds its terminal's width, the composition fills a meaningful share of the height instead of floating in a corner, the four waypoints and their real metadata are legible, `CAMP IS READY` and the next command appear only in the final frame, and the failure golden shows the real cause plus exactly one recovery command. If anything looks wrong, fix `scene.go` (Task 4/5) and re-run the capture — do not hand-edit a `.golden` file.

- [ ] **Step 4: Verify GREEN without the capture flag**

Run: `go test ./internal/presentation -count=1`

Expected: PASS — every golden test now compares against the reviewed, committed fixtures.

- [ ] **Step 5: Commit**

```bash
git add internal/presentation/terminal_golden_test.go internal/presentation/testdata/
git commit -m "test: capture reviewed Trailhead Topography goldens across supported sizes"
```

---

### Task 7: Integrate configuration prompts into the full-screen scene

**Files:**
- Modify: `internal/presentation/scene.go` (add `ConfigureAnswer`, `ComposeConfigureFrame`)
- Modify: `internal/cli/setup_prompt.go`
- Modify: `internal/cli/setup_prompt_test.go`
- Modify: `internal/cli/production_setup.go:82-107` (`Setup`, the prompt call site)

**Interfaces:**
- Consumes: `centerLine`/`skyRow`/`topographyRow`/`indentBlock`/`sceneContentWidth` (Tasks 2/4), `presentation.ScreenSize`, `presentation.TerminalExperience`.
- Produces: `presentation.ConfigureAnswer{Label, Value string}`, `presentation.ComposeConfigureFrame(answers []ConfigureAnswer, label, defaultValue string, size ScreenSize) string`, `promptSetupRequest(in io.Reader, out io.Writer, defaults setupPromptDefaults, experience presentation.TerminalExperience, size presentation.ScreenSize) (InitRequest, error)`.

- [ ] **Step 1: Write the failing tests**

Update the three existing tests in `internal/cli/setup_prompt_test.go` to pass the two new arguments — plain, unchanged behavior:

```go
func TestPromptSetupRequestUsesDefaultsAndDerivesCapsule(t *testing.T) {
	var output bytes.Buffer
	got, err := promptSetupRequest(strings.NewReader("\n\n\n\n\n"), &output, setupPromptDefaults{
		Source:  "/work/camp",
		Backend: "file:///home/test/.local/share/camp/backend",
	}, presentation.TerminalPlain, presentation.ScreenSize{})
	if err != nil {
		t.Fatalf("promptSetupRequest: %v", err)
	}
	want := InitRequest{
		Source: "/work/camp", Capsule: "camp",
		Backend:        "file:///home/test/.local/share/camp/backend",
		DevPodProvider: "docker", DevPodContext: "default",
	}
	if got != want {
		t.Fatalf("request = %#v, want %#v", got, want)
	}
}

func TestPromptSetupRequestUsesExplicitValues(t *testing.T) {
	got, err := promptSetupRequest(strings.NewReader("/srv/brain\nmemory\nfile:///srv/camp\npodman\nror\n"), &bytes.Buffer{}, setupPromptDefaults{
		Source: "/work/camp", Backend: "file:///home/test/.local/share/camp/backend",
	}, presentation.TerminalPlain, presentation.ScreenSize{})
	if err != nil {
		t.Fatalf("promptSetupRequest: %v", err)
	}
	want := InitRequest{
		Source: "/srv/brain", Capsule: "memory", Backend: "file:///srv/camp",
		DevPodProvider: "podman", DevPodContext: "ror",
	}
	if got != want {
		t.Fatalf("request = %#v, want %#v", got, want)
	}
}

func TestPromptSetupRequestRejectsEOF(t *testing.T) {
	if _, err := promptSetupRequest(strings.NewReader(""), &bytes.Buffer{}, setupPromptDefaults{
		Source: "/work/camp", Backend: "file:///home/test/.local/share/camp/backend",
	}, presentation.TerminalPlain, presentation.ScreenSize{}); err == nil || !strings.Contains(err.Error(), "source") {
		t.Fatalf("error = %v, want source EOF failure", err)
	}
}
```

Add `"github.com/joshyorko/camp/internal/presentation"` to the import block, then append:

```go
func TestPromptSetupRequestColorRendersIntegratedConfigureScene(t *testing.T) {
	var output bytes.Buffer
	got, err := promptSetupRequest(strings.NewReader("/work/camp\n\nfile:///store\n\n\n"), &output, setupPromptDefaults{
		Source: "/work/camp", Backend: "file:///home/test/.local/share/camp/backend",
	}, presentation.TerminalColor, presentation.ScreenSize{Width: 120, Height: 40})
	if err != nil {
		t.Fatalf("promptSetupRequest: %v", err)
	}
	if got.Capsule != "camp" || got.DevPodProvider != "docker" || got.DevPodContext != "default" {
		t.Fatalf("request = %#v", got)
	}
	rendered := output.String()
	if count := strings.Count(rendered, "\x1b[2J\x1b[H"); count != 5 {
		t.Fatalf("full-screen redraws = %d, want 5", count)
	}
	for _, want := range []string{"⛺ CAMP", "CONFIGURE", "Source path", "Capsule name", "Backend URL", "DevPod provider", "DevPod context"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("configure scene missing %q:\n%s", want, rendered)
		}
	}
}

func TestPromptSetupRequestColorEOFWritesNoPartialConfiguration(t *testing.T) {
	var output bytes.Buffer
	if _, err := promptSetupRequest(strings.NewReader(""), &output, setupPromptDefaults{
		Source: "/work/camp", Backend: "file:///home/test/.local/share/camp/backend",
	}, presentation.TerminalColor, presentation.ScreenSize{Width: 120, Height: 40}); err == nil {
		t.Fatal("promptSetupRequest accepted EOF")
	}
}
```

- [ ] **Step 2: Verify RED**

Run: `go test ./internal/cli -run TestPromptSetupRequest -count=1`

Expected: FAIL — `promptSetupRequest` still takes 3 arguments, and `presentation.ComposeConfigureFrame` doesn't exist.

- [ ] **Step 3: Implement the minimal integration**

Add to `internal/presentation/scene.go`:

```go
type ConfigureAnswer struct {
	Label string
	Value string
}

func ComposeConfigureFrame(answers []ConfigureAnswer, label, defaultValue string, size ScreenSize) string {
	width := size.Width
	if width < 80 {
		width = 80
	}
	contentWidth := sceneContentWidth(width, sceneMaxWidth)
	margin := (width - contentWidth) / 2

	var lines []string
	lines = append(lines, "")
	lines = append(lines, centerLine(colorCanvas+"⛺ CAMP"+colorReset, contentWidth))
	lines = append(lines, centerLine(colorDim+"first-run setup"+colorReset, contentWidth))
	lines = append(lines, "")
	lines = append(lines, skyRow(contentWidth))
	lines = append(lines, topographyRow(contentWidth))
	lines = append(lines, "")
	lines = append(lines, centerLine(colorAmber+"CONFIGURE"+colorReset, contentWidth))
	for _, answer := range answers {
		lines = append(lines, colorGreen+"✓ "+answer.Label+colorReset+colorCanvas+": "+answer.Value+colorReset)
	}
	lines = append(lines, colorAmber+"◐ "+label+colorReset+colorCanvas+" ["+defaultValue+"]: "+colorReset)

	return strings.Join(indentBlock(lines, margin), "\n")
}
```

This needs `"strings"` added to `internal/presentation/scene.go`'s import block. Note the return value has no trailing newline — the terminal cursor must stay at the end of the prompt line so the user's typed answer lands inline, matching the plain-mode behavior of `fmt.Fprintf(out, "%s [%s]: ", label, defaultValue)`.

Replace `internal/cli/setup_prompt.go` with:

```go
package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/joshyorko/camp/internal/presentation"
)

type setupPromptDefaults struct {
	Source  string
	Backend string
}

func promptSetupRequest(in io.Reader, out io.Writer, defaults setupPromptDefaults, experience presentation.TerminalExperience, size presentation.ScreenSize) (InitRequest, error) {
	reader := bufio.NewReader(in)
	var answers []presentation.ConfigureAnswer

	read := func(label, defaultValue string) (string, error) {
		if experience == presentation.TerminalColor {
			frame := "\x1b[2J\x1b[H" + presentation.ComposeConfigureFrame(answers, label, defaultValue, size)
			if _, err := io.WriteString(out, frame); err != nil {
				return "", err
			}
		} else if _, err := fmt.Fprintf(out, "%s [%s]: ", label, defaultValue); err != nil {
			return "", err
		}
		value, err := reader.ReadString('\n')
		if err != nil {
			return "", err
		}
		value = strings.TrimSpace(value)
		if value == "" {
			value = strings.TrimSpace(defaultValue)
		}
		answers = append(answers, presentation.ConfigureAnswer{Label: label, Value: value})
		return value, nil
	}

	source, err := read("Source path", defaults.Source)
	if err != nil {
		return InitRequest{}, fmt.Errorf("read source path: %w", err)
	}
	capsuleDefault := filepath.Base(filepath.Clean(source))
	capsule, err := read("Capsule name", capsuleDefault)
	if err != nil {
		return InitRequest{}, fmt.Errorf("read capsule name: %w", err)
	}
	backend, err := read("Backend URL", defaults.Backend)
	if err != nil {
		return InitRequest{}, fmt.Errorf("read backend URL: %w", err)
	}
	provider, err := read("DevPod provider", "docker")
	if err != nil {
		return InitRequest{}, fmt.Errorf("read DevPod provider: %w", err)
	}
	devpodContext, err := read("DevPod context", "default")
	if err != nil {
		return InitRequest{}, fmt.Errorf("read DevPod context: %w", err)
	}
	request := InitRequest{
		Source: source, Capsule: capsule, Backend: backend,
		DevPodProvider: provider, DevPodContext: devpodContext,
	}
	if request.Source == "" || request.Capsule == "" || request.Backend == "" || request.DevPodProvider == "" || request.DevPodContext == "" {
		return InitRequest{}, errors.New("setup values cannot be empty")
	}
	return request, nil
}
```

In `internal/cli/production_setup.go`, in `Setup`, move the `resolveTerminalExperience` call above the prompt block and pass `experience`/size through:

```go
func (p *ProductionLifecycle) Setup(ctx context.Context, mode OutputMode, in io.Reader, out io.Writer) error {
	environment := environmentMap(os.Environ())
	paths, err := config.ResolveXDGPaths(config.XDGInput{Environment: environment})
	if err != nil {
		return err
	}
	experience, width, height := resolveTerminalExperience(mode, out, environment, probeTerminal)
	if mode == ModeHuman {
		if _, statErr := os.Stat(paths.ConfigPath); statErr != nil {
			if !os.IsNotExist(statErr) {
				return statErr
			}
			source, err := os.Getwd()
			if err != nil {
				return err
			}
			request, err := promptSetupRequest(in, out, setupPromptDefaults{
				Source: source, Backend: "file://" + filepath.Join(paths.DataRoot, "backend"),
			}, experience, presentation.ScreenSize{Width: width, Height: height})
			if err != nil {
				return err
			}
			if err := p.Init(ctx, request, mode, io.Discard); err != nil {
				return err
			}
		}
	}
	lockBytes := campcontract.DistributionToolLock()
	completed := func(name string, resolution tooladapter.Resolution) error {
		return writeLifecycleEvents(out, experience, "setup", presentation.LifecycleEvent{Stage: presentation.StageToolReady, Message: fmt.Sprintf("%s %s is ready", name, resolution.Version)})
	}
	if mode == ModeJSON {
		completed = nil
	}
	if err := runProductionToolSetupWithEvents(ctx, mode, out, lockBytes, "", environment, runtime.GOOS, runtime.GOARCH, completed); err != nil || mode == ModeJSON {
		return err
	}
	return renderProductionSetupCampsite(ctx, out, lockBytes, experience, width, height)
}
```

(this removes the now-duplicate `resolveTerminalExperience` call that Task 1 left in place lower in the function — there is only one call now, at the top).

- [ ] **Step 4: Verify GREEN**

Run: `go build ./... && go test ./internal/cli ./internal/presentation -count=1`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/presentation/scene.go internal/cli/setup_prompt.go internal/cli/setup_prompt_test.go internal/cli/production_setup.go
git commit -m "feat: integrate first-run configuration prompts into the full-screen scene"
```

---

### Task 8: Documentation, verification, exact binary rebuild, and PR

**Files:**
- Modify: `docs/skills/terminal-experience.md`

**Interfaces:**
- Consumes: verified behavior from Tasks 1–7.
- Produces: durable operator guidance and release evidence (the acceptance receipt requested by the task).

- [ ] **Step 1: Update canonical guidance**

Append to `docs/skills/terminal-experience.md` (after the existing golden-coverage paragraph, i.e. after line 33):

```markdown

Color composition is measured, not hand-typed. `internal/presentation/layout.go` strips ANSI to compute visible width, centers by left-padding only (never trailing spaces, which would leave whitespace in committed goldens), and lays out the four waypoints as a measured column block rather than literal-space strings. The scene composer (`internal/presentation/scene.go`) is the single renderer behind the static campsite, every in-progress waypoint frame, the ready state, and failures — the setup animator only ever changes which `WaypointStatus` (`pending`, `active`, `completed`, `failed`) and which optional failure it passes in, so no stage of the experience is a visually disconnected fragment. The composed scene letterboxes to at most 96 measured columns and vertically distributes blank margins across the probed terminal height (`internal/cli/terminal_linux.go`'s `probeTerminal` reads winsize rows as well as columns); terminals shorter than 20 rows drop the decorative sky/topography rows to stay legible without clipping. `CAMP IS READY` and the next command remain gated to the frame where `ready` is true and no failure is present; a failed waypoint replaces that band with the real error message and exactly one recovery command, never both.

Configuration prompts (source, capsule, backend, DevPod provider, DevPod context) render inside the same full-screen scene once a true-color terminal is detected, each redraw showing prior answers in a `CONFIGURE` panel and the active prompt with its default on the last line so the terminal's own cursor — never manually positioned — stays the visible focus indicator. Plain, JSON, and narrow terminals keep the original line-based `Label [default]: ` prompts byte-for-byte; EOF or an empty required answer still fails before any byte is written to persisted configuration.
```

- [ ] **Step 2: Run complete gates**

Run, in order:

```bash
go test ./internal/presentation ./internal/cli -run 'TestComposeScene|TestWaypointStatus|TestVisibleWidth|TestCenterLine|TestHorizontalRule|TestEvenlySpaced|TestOverlayGlyphs|TestColumnBlock|TestDistributeVertically|TestIndentBlock|TestSceneContentWidth|TestSetupAnimator|TestPromptSetupRequest|TestResolveTerminalExperience' -count=1 -v
go test ./... -count=1
go test -race ./... -count=1
go vet ./...
go build -o /tmp/camp-visual-repair ./cmd/camp
git diff --check
```

Expected: all pass with zero diffs flagged by `git diff --check` (no trailing whitespace in any modified `.go` or `testdata/*.golden` file — this is exactly why `centerLine`/`columnBlock`/`indentBlock` were written to never leave trailing spaces).

- [ ] **Step 3: Rebuild the exact branch binary and capture the real experience in a PTY**

```bash
go build -ldflags "-X main.version=$(git describe --tags --always --dirty)" -o ~/.local/bin/camp ./cmd/camp
camp --version
```

Then, in an isolated `XDG_DATA_HOME`/`XDG_CONFIG_HOME` fixture pointed at a scratch capsule directory, drive `camp setup` inside a PTY sized at each required dimension (e.g. with `script`, `tmux`, or a small Go PTY harness using `github.com/creack/pty` run ad hoc — do not add it as a module dependency for this) and capture the screen at: 80×24, 120×40, and the supplied screenshot's approximate geometry (documented above as ~130×50). Save the captures alongside the plan's acceptance receipt.

- [ ] **Step 4: Visually compare against the supplied failure screenshot**

Confirm, by eye, against `/var/home/kdlocpanda/Pictures/Screenshots/Screenshot From 2026-07-23 22-37-25.png`: the new capture fills a deliberate, height-aware composition (not an eleven-line island); the topography reads as an intentional campsite, not disconnected punctuation; the four waypoints show real grouped metadata with visibly distinct pending/active/completed/failed markers; the route visually connects to the ready band; `CAMP IS READY` and the next command form one strong closing panel. If it still looks like a status dump, return to Task 4/5 — passing goldens are not sufficient evidence of completion.

- [ ] **Step 5: File the issue, open the PR**

```bash
gh issue create --title "camp setup: Trailhead Topography scene reads as a debug transcript, not a campsite" \
  --body "Visual repair of the color first-run experience shipped in #35/#46/#49. See attached failure screenshot: tiny disconnected content floating in unused terminal space, no measured layout, no terminal-height awareness, only two waypoint states, setup prompts disconnected from the animated scene."
git checkout -b patchraptor/setup-trailhead-visual-repair
git push -u origin patchraptor/setup-trailhead-visual-repair
gh pr create --title "fix: compose a measured, height-aware Trailhead Topography scene" \
  --body "Closes #<issue-number>. Replaces the literal-space, two-state, height-blind renderer with a measured layout toolkit and one shared scene composer used by prompts, waypoints, the ready state, and failures. See plan at docs/superpowers/plans/2026-07-24-first-run-terminal-visual-repair.md for verification evidence."
```

(Open the issue first, note its number, then reference it in the branch's first commit message or PR body — do not merge; leave the PR open for review per the requested workflow, which stops at "capture and compare," not merge.)

- [ ] **Step 6: Commit the documentation update**

```bash
git add docs/skills/terminal-experience.md
git commit -m "docs: describe the measured Trailhead Topography scene composer"
git push
```
