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
