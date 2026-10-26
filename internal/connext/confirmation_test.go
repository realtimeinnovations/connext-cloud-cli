// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

package connext

import (
	"errors"
	"testing"
)

func TestConfirmationAdapter(t *testing.T) {
	if ConfirmationFromSelector(nil) != nil {
		t.Fatal("nil selector must preserve noninteractive handling")
	}
	request := Confirmation{Message: "Proceed?", DeclineLabel: "Cancel", AcceptLabel: "Accept"}
	inputErr := errors.New("input closed")
	for _, test := range []struct {
		choice              string
		err                 error
		accepted, wantError bool
		defaultAccept       bool
	}{
		{"Accept", nil, true, false, false}, {"Cancel", nil, false, false, false}, {"unexpected", nil, false, true, false}, {"", inputErr, false, true, false},
		{"Accept", nil, true, false, true}, {"Cancel", nil, false, false, true},
	} {
		request.DefaultAccept = test.defaultAccept
		confirmer := ConfirmationFromSelector(func(message string, choices []string, defaultChoice string) (string, error) {
			first, second := "Cancel", "Accept"
			if test.defaultAccept {
				first, second = second, first
			}
			if message != request.Message || len(choices) != 2 || choices[0] != first || choices[1] != second || defaultChoice != first {
				t.Fatalf("unexpected request: %s %v", message, choices)
			}
			return test.choice, test.err
		})
		accepted, err := confirmer.Confirm(request)
		if accepted != test.accepted || (err != nil) != test.wantError {
			t.Fatalf("%q: %v %v", test.choice, accepted, err)
		}
		if test.err != nil && !errors.Is(err, test.err) {
			t.Fatal("lost input error")
		}
	}
}
