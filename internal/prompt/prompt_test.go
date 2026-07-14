// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

package prompt

import (
	"bytes"
	"strings"
	"testing"
)

func TestSelectorUsesNumberedFallback(t *testing.T) {
	var out bytes.Buffer
	selector := Selector{In: strings.NewReader("2\n"), Out: &out}
	selected, err := selector.Select("Select item:", []string{"one", "two"})
	if err != nil {
		t.Fatal(err)
	}
	if selected != "two" {
		t.Fatalf("unexpected selection: %s", selected)
	}
	if !strings.Contains(out.String(), "Select item:") || !strings.Contains(out.String(), "2. two") {
		t.Fatalf("unexpected output: %s", out.String())
	}
}

func TestSelectorUsesNumberedFallbackWithDetails(t *testing.T) {
	var out bytes.Buffer
	selector := Selector{In: strings.NewReader("1\n"), Out: &out}
	selected, err := selector.Select("Select item:", []string{ChoiceWithLabel("one", "one\nfirst.example.com"), ChoiceWithLabel("two", "two\nsecond.example.com")})
	if err != nil {
		t.Fatal(err)
	}
	if selected != "one" {
		t.Fatalf("unexpected selection: %s", selected)
	}
	rendered := strings.ReplaceAll(out.String(), "\x1b[2m", "")
	rendered = strings.ReplaceAll(rendered, "\x1b[0m", "")
	checks := []string{"1. one", "first.example.com", "2. two", "second.example.com"}
	for _, check := range checks {
		if !strings.Contains(rendered, check) {
			t.Fatalf("missing %q in output: %s", check, rendered)
		}
	}
}

func TestSelectorReturnsValueForLabeledSingleChoice(t *testing.T) {
	selector := Selector{SpecialLabels: map[string]string{"create": "Create new..."}}
	selected, err := selector.Select("Select item:", []string{ChoiceWithLabel("create", "Create new...")})
	if err != nil {
		t.Fatal(err)
	}
	if selected != "create" {
		t.Fatalf("unexpected selection: %s", selected)
	}
}

func TestSelectorUsesDefaultForBlankSelection(t *testing.T) {
	var out bytes.Buffer
	selector := Selector{In: strings.NewReader("\n"), Out: &out, DefaultChoice: "two"}
	selected, err := selector.Select("Select item:", []string{"one", "two"})
	if err != nil {
		t.Fatal(err)
	}
	if selected != "two" {
		t.Fatalf("unexpected selection: %s", selected)
	}
	if !strings.Contains(out.String(), "Select an option [two]:") {
		t.Fatalf("unexpected output: %s", out.String())
	}
}

func TestInputUsesLineFallback(t *testing.T) {
	var out bytes.Buffer
	input := Input{In: strings.NewReader("/tmp/connext\n"), Out: &out}
	value, err := input.Prompt("Enter Connext path:")
	if err != nil {
		t.Fatal(err)
	}
	if value != "/tmp/connext" {
		t.Fatalf("unexpected value: %s", value)
	}
	if !strings.Contains(out.String(), "Enter Connext path:") {
		t.Fatalf("unexpected output: %s", out.String())
	}
}

func TestInputUsesDefaultForBlankValue(t *testing.T) {
	var out bytes.Buffer
	input := Input{In: strings.NewReader("\n"), Out: &out, DefaultValue: "/tmp/default"}
	value, err := input.Prompt("Enter Connext path:")
	if err != nil {
		t.Fatal(err)
	}
	if value != "/tmp/default" {
		t.Fatalf("unexpected value: %s", value)
	}
}

func TestSplitPromptMessageSingleLine(t *testing.T) {
	prefix, label := splitPromptMessage("Select Connext installation:")
	if prefix != "" || label != "Select Connext installation:" {
		t.Fatalf("unexpected split: %q / %q", prefix, label)
	}
}

func TestSplitPromptMessageMultiLine(t *testing.T) {
	message := "Connext Pro 7.7.0 or newer with rtiroutingservice was not found.\n\nTo use an installation in a non-standard directory, export NDDSHOME before.\n\nSelect how to continue:"
	prefix, label := splitPromptMessage(message)
	if !strings.Contains(prefix, "NDDSHOME") {
		t.Fatalf("unexpected prefix: %q", prefix)
	}
	if label != "Select how to continue:" {
		t.Fatalf("unexpected label: %q", label)
	}
}

func TestFitItemsToWidth_TruncatesToOneLine(t *testing.T) {
	long := "ces-farm-greenfield-d06522276c4142f4942a9f7bccfe6ae2 / 29:north-field / tractor (serial MF-4700-001)"
	items := []selectItem{
		{Label: long},
		{Label: "short", Detail: long},
	}
	const width = 40
	fitItemsToWidth(items, width)

	// The "▸ " active prefix (2) plus the label must fit within the width, with
	// a column to spare for the terminal's auto-wrap edge.
	if w := len([]rune(items[0].Label)); w > width-3 {
		t.Fatalf("label width %d exceeds budget %d: %q", w, width-3, items[0].Label)
	}
	if !strings.HasSuffix(items[0].Label, "…") {
		t.Fatalf("expected truncated label to end with ellipsis, got %q", items[0].Label)
	}
	if items[1].Label != "short" {
		t.Fatalf("short label should be untouched, got %q", items[1].Label)
	}
	if w := len([]rune(items[1].Detail)); w > width-5 {
		t.Fatalf("detail width %d exceeds budget %d", w, width-5)
	}
}

func TestFitItemsToWidth_NonPositiveWidthNoOp(t *testing.T) {
	long := strings.Repeat("x", 200)
	items := []selectItem{{Label: long}}
	fitItemsToWidth(items, 0)
	if items[0].Label != long {
		t.Fatal("width 0 must leave labels unchanged")
	}
}
