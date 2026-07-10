package diagnostics

import "strings"

type Severity string

const (
	SeverityWarning Severity = "warning"
	SeverityError   Severity = "error"
)

type Rule struct {
	ID       string
	Contains string
	Severity Severity
	Summary  func(string) string
	Hint     string
}

type Finding struct {
	ID       string
	Severity Severity
	Summary  string
	Hint     string
	Source   string
	Count    int
	RawLine  string
}

type Detector struct {
	rules    []Rule
	findings []Finding
	byID     map[string]int
}

func NewDetector() *Detector {
	return NewDetectorWithRules(DefaultRules())
}

func NewDetectorWithRules(rules []Rule) *Detector {
	return &Detector{
		rules: append([]Rule(nil), rules...),
		byID:  make(map[string]int),
	}
}

// Observe records every matching line and returns the matching finding. The
// boolean is true only for the first occurrence, which lets text-mode callers
// print each hint once while the live TUI continues updating its occurrence
// count.
func (detector *Detector) Observe(source string, line string) (Finding, bool) {
	if detector == nil {
		return Finding{}, false
	}
	for _, rule := range detector.rules {
		if rule.Contains == "" || !strings.Contains(line, rule.Contains) {
			continue
		}
		summary := rule.ID
		if rule.Summary != nil {
			summary = rule.Summary(line)
		}
		key := rule.ID + "\x00" + summary
		if index, exists := detector.byID[key]; exists {
			detector.findings[index].Count++
			return detector.findings[index], false
		}
		finding := Finding{
			ID:       rule.ID,
			Severity: rule.Severity,
			Summary:  summary,
			Hint:     rule.Hint,
			Source:   source,
			Count:    1,
			RawLine:  line,
		}
		detector.byID[key] = len(detector.findings)
		detector.findings = append(detector.findings, finding)
		return finding, true
	}
	return Finding{}, false
}

func (detector *Detector) Findings() []Finding {
	if detector == nil {
		return nil
	}
	return append([]Finding(nil), detector.findings...)
}
