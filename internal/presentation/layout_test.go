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
