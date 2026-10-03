// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

// Package doctor inspects local prerequisites and performs a bounded Cloud read.
// It never installs, migrates, saves credentials, or launches Connext processes.
package doctor

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode"

	"github.com/realtimeinnovations/connext-cloud-cli/internal/clierror"
)

type Detail struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

type Check struct {
	ID             string           `json:"id"`
	Label          string           `json:"label"`
	Status         string           `json:"status"`
	Message        string           `json:"message"`
	Code           string           `json:"code,omitempty"`
	RequiredAction string           `json:"required_action,omitempty"`
	NextStep       string           `json:"next_step,omitempty"`
	BlockedBy      []string         `json:"blocked_by,omitempty"`
	Details        []Detail         `json:"details,omitempty"`
	HTTPStatus     int              `json:"http_status,omitempty"`
	DurationMS     int64            `json:"duration_ms,omitempty"`
	Features       []LicenseFeature `json:"features,omitempty"`
}

type Summary struct {
	Passed   int `json:"passed"`
	Warnings int `json:"warnings"`
	Failed   int `json:"failed"`
	Skipped  int `json:"skipped"`
}

type Report struct {
	Healthy  bool    `json:"healthy"`
	ExitCode int     `json:"exit_code"`
	Summary  Summary `json:"summary"`
	Checks   []Check `json:"checks"`
}

func (r *Report) add(c Check) {
	r.Checks = append(r.Checks, c)
	switch c.Status {
	case "pass":
		r.Summary.Passed++
	case "warn":
		r.Summary.Warnings++
	case "skip":
		r.Summary.Skipped++
	case "fail":
		r.Summary.Failed++
		// Stable precedence: first failing check in report order determines exit.
		if r.ExitCode == 0 {
			r.ExitCode = (&clierror.Error{Code: c.Code}).ExitCode()
		}
	}
	r.Healthy = r.Summary.Failed == 0
}

// CompletedError signals an unhealthy report already written to stdout.
// CLI integration must return its exit code without emitting a second document.
type CompletedError struct{ ExitCode int }

func (e *CompletedError) Error() string { return "doctor found failing checks" }

func (r Report) Write(out io.Writer, format string) error {
	if format == "json" {
		return json.NewEncoder(out).Encode(struct {
			SchemaVersion string `json:"schema_version"`
			Data          Report `json:"data"`
		}{"1", r})
	}
	var b strings.Builder
	b.WriteString("RTI Connext Cloud · Doctor\n\n")
	symbols := map[string]string{"pass": "✓", "warn": "!", "fail": "✗", "skip": "–"}
	for _, c := range r.Checks {
		fmt.Fprintf(&b, "%s %s  %s\n", symbols[c.Status], c.Label, safeText(c.Message))
		for _, d := range c.Details {
			fmt.Fprintf(&b, "  %-14s %s\n", d.Label, safeText(d.Value))
		}
		if c.HTTPStatus != 0 {
			fmt.Fprintf(&b, "  %-14s HTTP %d · %d ms\n", "Result", c.HTTPStatus, c.DurationMS)
		} else if c.DurationMS > 0 {
			fmt.Fprintf(&b, "  %-14s %d ms\n", "Elapsed", c.DurationMS)
		}
		for _, f := range c.Features {
			fmt.Fprintf(&b, "  %-14s %s\n", safeText(f.Feature), f.description())
		}
		b.WriteByte('\n')
	}
	seen := map[string]bool{}
	for _, status := range []string{"fail", "warn", "skip"} {
		for _, c := range r.Checks {
			if c.Status == status && c.NextStep != "" && !seen[c.NextStep] {
				if len(seen) == 0 {
					b.WriteString("Next steps\n")
				}
				fmt.Fprintf(&b, "  %s\n", safeText(c.NextStep))
				seen[c.NextStep] = true
			}
		}
	}
	if len(seen) > 0 {
		b.WriteByte('\n')
	}
	fmt.Fprintf(&b, "%d passed · %d warnings · %d failed · %d skipped\n", r.Summary.Passed, r.Summary.Warnings, r.Summary.Failed, r.Summary.Skipped)
	b.WriteString("Local checks inspect prerequisites; Connext validates licenses and connectivity when it runs.\n")
	_, err := io.WriteString(out, b.String())
	return err
}

func safeText(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
}

func detail(label, value string) Detail { return Detail{label, value} }
func skip(id, label, message string, dependencies ...string) Check {
	return Check{ID: id, Label: label, Status: "skip", Message: message, BlockedBy: dependencies}
}
