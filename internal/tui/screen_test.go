package tui

import (
	"bytes"
	"strings"
	"testing"
)

func TestScreenFirstPaintHidesCursorAndAvoidsScreenClear(t *testing.T) {
	var out bytes.Buffer
	screen := NewScreen(&out)
	if err := screen.Paint([]string{"one", "two"}, 80, 24); err != nil {
		t.Fatal(err)
	}
	rendered := out.String()
	for _, sequence := range []string{beginSynchronizedUpdate, hideCursorSequence, "\x1b[1;1H\x1b[Kone", "\x1b[2;1H\x1b[Ktwo", endSynchronizedUpdate} {
		if !strings.Contains(rendered, sequence) {
			t.Fatalf("missing %q in first paint %q", sequence, rendered)
		}
	}
	if strings.Contains(rendered, "\x1b[2J") || strings.Contains(rendered, "\x1b[H\x1b[J") {
		t.Fatalf("first paint should not clear the whole screen: %q", rendered)
	}
	if !strings.Contains(rendered, "\x1b[3;1H\x1b[J") {
		t.Fatalf("first paint should erase leftovers below the frame: %q", rendered)
	}
}

func TestScreenRepaintsOnlyChangedRows(t *testing.T) {
	var out bytes.Buffer
	screen := NewScreen(&out)
	if err := screen.Paint([]string{"one", "two", "three"}, 80, 24); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := screen.Paint([]string{"one", "TWO", "three"}, 80, 24); err != nil {
		t.Fatal(err)
	}
	rendered := out.String()
	if !strings.Contains(rendered, "\x1b[2;1H\x1b[KTWO") {
		t.Fatalf("expected changed row rewrite in %q", rendered)
	}
	if strings.Contains(rendered, "one") || strings.Contains(rendered, "three") {
		t.Fatalf("unchanged rows should not be rewritten: %q", rendered)
	}
}

func TestScreenSkipsWriteWhenFrameIsUnchanged(t *testing.T) {
	var out bytes.Buffer
	screen := NewScreen(&out)
	if err := screen.Paint([]string{"one", "two"}, 80, 24); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := screen.Paint([]string{"one", "two"}, 80, 24); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Fatalf("expected no bytes for identical frame, got %q", out.String())
	}
}

func TestScreenErasesRowsLeftOverFromTallerFrame(t *testing.T) {
	var out bytes.Buffer
	screen := NewScreen(&out)
	if err := screen.Paint([]string{"one", "two", "three"}, 80, 24); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := screen.Paint([]string{"one"}, 80, 24); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "\x1b[2;1H\x1b[J") {
		t.Fatalf("expected erase below shorter frame in %q", out.String())
	}
}

func TestScreenRepaintsEverythingOnResize(t *testing.T) {
	var out bytes.Buffer
	screen := NewScreen(&out)
	if err := screen.Paint([]string{"one", "two"}, 80, 24); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := screen.Paint([]string{"one", "two"}, 100, 30); err != nil {
		t.Fatal(err)
	}
	rendered := out.String()
	if !strings.Contains(rendered, "\x1b[1;1H\x1b[Kone") || !strings.Contains(rendered, "\x1b[2;1H\x1b[Ktwo") {
		t.Fatalf("expected full repaint after resize, got %q", rendered)
	}
}

func TestScreenClampsFrameToTerminalHeight(t *testing.T) {
	var out bytes.Buffer
	screen := NewScreen(&out)
	if err := screen.Paint([]string{"one", "two", "three", "four"}, 80, 3); err != nil {
		t.Fatal(err)
	}
	rendered := out.String()
	if strings.Contains(rendered, "four") {
		t.Fatalf("rows beyond the terminal height should be dropped: %q", rendered)
	}
	if !strings.Contains(rendered, "\x1b[3;1H\x1b[Kthree") {
		t.Fatalf("expected last visible row to be painted: %q", rendered)
	}
}

func TestScreenFinishRestoresCursorBelowFrameOnce(t *testing.T) {
	var out bytes.Buffer
	screen := NewScreen(&out)
	if err := screen.Paint([]string{"one", "two"}, 80, 24); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := screen.Finish(); err != nil {
		t.Fatal(err)
	}
	if out.String() != "\x1b[2;1H\n"+showCursorSequence {
		t.Fatalf("unexpected finish sequence %q", out.String())
	}
	out.Reset()
	if err := screen.Finish(); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Fatalf("second finish should be a no-op, got %q", out.String())
	}
}

func TestScreenFinishBeforeFirstPaintIsNoOp(t *testing.T) {
	var out bytes.Buffer
	screen := NewScreen(&out)
	if err := screen.Finish(); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Fatalf("finish without paint should write nothing, got %q", out.String())
	}
}
