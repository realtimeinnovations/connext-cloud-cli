// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

package prompt

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/manifoldco/promptui"
	"github.com/realtimeinnovations/connext-cloud-cli/common"
	"github.com/realtimeinnovations/connext-cloud-cli/internal/terminal"
	"github.com/realtimeinnovations/connext-cloud-cli/internal/tui"
)

const choiceSeparator = "\x00"

type Selector struct {
	In            io.Reader
	Out           io.Writer
	CancelMessage string
	DefaultChoice string
	SpecialLabels map[string]string
}

type Input struct {
	In            io.Reader
	Out           io.Writer
	CancelMessage string
	DefaultValue  string
}

type selectItem struct {
	Label  string
	Detail string
}

func (selector Selector) Select(message string, choices []string) (string, error) {
	if len(choices) == 0 {
		return "", nil
	}
	if len(choices) == 1 {
		return SelectionValue(choices[0]), nil
	}
	if inputFile, outputFile, ok := terminal.PromptFiles(selector.input(), selector.Out); ok {
		selected, err := selector.cursorSelect(message, choices, inputFile, outputFile)
		if err == nil {
			return selected, nil
		}
		if errors.Is(err, promptui.ErrInterrupt) || errors.Is(err, promptui.ErrEOF) {
			return "", common.UserError{Message: selector.cancelMessage()}
		}
		return "", err
	}
	return selector.numberedSelect(message, choices)
}

func (selector Selector) numberedSelect(message string, choices []string) (string, error) {
	reader := bufferedReader(selector.input())
	for {
		_, _ = fmt.Fprintln(selector.Out, message)
		for idx, choice := range choices {
			selector.printNumberedChoice(idx+1, selector.SelectionLabel(choice))
		}
		_, _ = fmt.Fprint(selector.Out, selector.selectionPrompt(choices))
		line, err := reader.ReadString('\n')
		if err != nil && err != io.EOF {
			return "", err
		}
		input := strings.TrimSpace(line)
		if input == "" {
			if selected, ok := selector.defaultSelection(choices); ok {
				return selected, nil
			}
			if err == io.EOF {
				return "", common.UserError{Message: selector.cancelMessage()}
			}
			continue
		}
		if index, ok := parseChoiceIndex(input, len(choices)); ok {
			return SelectionValue(choices[index]), nil
		}
		for _, choice := range choices {
			label := selector.SelectionLabel(choice)
			if input == choice || input == SelectionValue(choice) || input == label || input == selectionTitle(label) {
				return SelectionValue(choice), nil
			}
		}
		_, _ = fmt.Fprintln(selector.Out, "Invalid selection. Enter the option number.")
		if err == io.EOF {
			return "", common.UserError{Message: selector.cancelMessage()}
		}
	}
}

func (selector Selector) cursorSelect(message string, choices []string, inputFile io.ReadCloser, outputFile io.WriteCloser) (string, error) {
	items := make([]selectItem, 0, len(choices))
	hasDetails := false
	for _, choice := range choices {
		item := newSelectItem(selector.SelectionLabel(choice))
		if item.Detail != "" {
			hasDetails = true
		}
		items = append(items, item)
	}
	prefix, label := splitPromptMessage(message)
	if prefix != "" {
		_, _ = fmt.Fprintln(outputFile, prefix)
		_, _ = fmt.Fprintln(outputFile)
	}
	templates := &promptui.SelectTemplates{
		Label:    "{{ . }}",
		Active:   "\x1b[38;5;208m▸ {{ .Label }}\x1b[0m",
		Inactive: "  {{ .Label }}",
		Selected: "\x1b[38;5;208m▸ {{ .Label }}\x1b[0m",
	}
	if hasDetails {
		templates.Details = "    \x1b[2m{{ .Detail }}\x1b[0m"
	}
	prompt := promptui.Select{
		Label:     label,
		Items:     items,
		Size:      minInt(len(items), 10),
		CursorPos: selector.defaultCursorPos(choices),
		HideHelp:  true,
		Stdin:     inputFile,
		Stdout:    outputFile,
		Templates: templates,
	}
	index, _, err := prompt.Run()
	if err != nil {
		return "", err
	}
	return SelectionValue(choices[index]), nil
}

func (selector Selector) printNumberedChoice(index int, label string) {
	title, detail := splitSelectionLabel(label)
	prefix := fmt.Sprintf("  %d. ", index)
	_, _ = fmt.Fprintf(selector.Out, "%s%s\n", prefix, title)
	if detail == "" {
		return
	}
	indent := strings.Repeat(" ", len(prefix))
	for _, line := range strings.Split(detail, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		_, _ = fmt.Fprintf(selector.Out, "%s%s\n", indent, tui.Dim(strings.TrimSpace(line)))
	}
}

func newSelectItem(label string) selectItem {
	title, detail := splitSelectionLabel(label)
	return selectItem{Label: title, Detail: detail}
}

func selectionTitle(label string) string {
	title, _ := splitSelectionLabel(label)
	return title
}

func splitSelectionLabel(label string) (string, string) {
	parts := strings.SplitN(label, "\n", 2)
	if len(parts) == 1 {
		return strings.TrimSpace(parts[0]), ""
	}
	return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
}

func (selector Selector) SelectionLabel(choice string) string {
	if value, label, ok := SplitChoice(choice); ok {
		if specialLabel := selector.SpecialLabels[value]; specialLabel != "" {
			return specialLabel
		}
		return label
	}
	if specialLabel := selector.SpecialLabels[choice]; specialLabel != "" {
		return specialLabel
	}
	return choice
}

func (selector Selector) defaultSelection(choices []string) (string, bool) {
	if selector.DefaultChoice == "" {
		return "", false
	}
	for _, choice := range choices {
		if SelectionValue(choice) == selector.DefaultChoice {
			return selector.DefaultChoice, true
		}
	}
	return "", false
}

func (selector Selector) defaultCursorPos(choices []string) int {
	if selector.DefaultChoice == "" {
		return 0
	}
	for idx, choice := range choices {
		if SelectionValue(choice) == selector.DefaultChoice {
			return idx
		}
	}
	return 0
}

func (selector Selector) selectionPrompt(choices []string) string {
	for _, choice := range choices {
		if SelectionValue(choice) == selector.DefaultChoice {
			return fmt.Sprintf("Select an option [%s]: ", selectionTitle(selector.SelectionLabel(choice)))
		}
	}
	return "Select an option: "
}

func ChoiceWithLabel(value string, label string) string {
	return value + choiceSeparator + label
}

func SelectionValue(choice string) string {
	if value, _, ok := SplitChoice(choice); ok {
		return value
	}
	return choice
}

func SplitChoice(choice string) (string, string, bool) {
	parts := strings.SplitN(choice, choiceSeparator, 2)
	if len(parts) != 2 {
		return "", "", false
	}
	return parts[0], parts[1], true
}

func (selector Selector) input() io.Reader {
	if selector.In != nil {
		return selector.In
	}
	return os.Stdin
}

func (selector Selector) cancelMessage() string {
	if selector.CancelMessage != "" {
		return selector.CancelMessage
	}
	return "Selection cancelled."
}

func parseChoiceIndex(input string, choiceCount int) (int, bool) {
	index, err := strconv.Atoi(input)
	if err != nil {
		return 0, false
	}
	if index < 1 || index > choiceCount {
		return 0, false
	}
	return index - 1, true
}

func minInt(left int, right int) int {
	if left < right {
		return left
	}
	return right
}

func (input Input) Prompt(message string) (string, error) {
	if inputFile, outputFile, ok := terminal.PromptFiles(input.input(), input.Out); ok {
		value, err := input.cursorPrompt(message, inputFile, outputFile)
		if err == nil {
			return value, nil
		}
		if errors.Is(err, promptui.ErrInterrupt) || errors.Is(err, promptui.ErrEOF) {
			return "", common.UserError{Message: input.cancelMessage()}
		}
		return "", err
	}
	return input.linePrompt(message)
}

func (input Input) linePrompt(message string) (string, error) {
	reader := bufferedReader(input.input())
	_, _ = fmt.Fprintln(input.Out, message)
	if input.DefaultValue != "" {
		_, _ = fmt.Fprintf(input.Out, "Value [%s]: ", input.DefaultValue)
	} else {
		_, _ = fmt.Fprint(input.Out, "Value: ")
	}
	line, err := reader.ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	value := strings.TrimSpace(line)
	if value == "" && input.DefaultValue != "" {
		value = input.DefaultValue
	}
	if value == "" && err == io.EOF {
		return "", common.UserError{Message: input.cancelMessage()}
	}
	return value, nil
}

func (input Input) cursorPrompt(message string, inputFile io.ReadCloser, outputFile io.WriteCloser) (string, error) {
	prefix, label := splitPromptMessage(message)
	if prefix != "" {
		_, _ = fmt.Fprintln(outputFile, prefix)
		_, _ = fmt.Fprintln(outputFile)
	}
	prompt := promptui.Prompt{
		Label:   label,
		Default: input.DefaultValue,
		Stdin:   inputFile,
		Stdout:  outputFile,
	}
	return prompt.Run()
}

func (input Input) input() io.Reader {
	if input.In != nil {
		return input.In
	}
	return os.Stdin
}

func (input Input) cancelMessage() string {
	if input.CancelMessage != "" {
		return input.CancelMessage
	}
	return "Input cancelled."
}

func splitPromptMessage(message string) (string, string) {
	trimmed := strings.TrimRight(message, "\n")
	if trimmed == "" {
		return "", ""
	}
	index := strings.LastIndex(trimmed, "\n")
	if index < 0 {
		return "", trimmed
	}
	return strings.TrimRight(trimmed[:index], "\n"), trimmed[index+1:]
}

func bufferedReader(input io.Reader) *bufio.Reader {
	if reader, ok := input.(*bufio.Reader); ok {
		return reader
	}
	return bufio.NewReader(input)
}
