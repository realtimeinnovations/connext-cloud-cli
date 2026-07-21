// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

package httputil

import (
	"encoding/json"
	"fmt"
	"strings"
)

// StatusError is a non-2xx HTTP response carried as an error with its status
// code intact, so callers can branch on the code instead of matching substrings
// of Message — which comes from the response body and can say anything.
type StatusError struct {
	StatusCode int
	Message    string
}

// Error renders the same "HTTP <code>: <message>" text these call sites
// produced before the type existed.
func (e *StatusError) Error() string {
	return fmt.Sprintf("HTTP %d: %s", e.StatusCode, e.Message)
}

// NewStatusError builds a StatusError from a response status and body.
func NewStatusError(statusCode int, body []byte) *StatusError {
	return &StatusError{StatusCode: statusCode, Message: FormatError(statusCode, body)}
}

func FormatError(statusCode int, body []byte) string {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err == nil {
		for _, key := range []string{"error", "message", "detail", "description"} {
			if value, ok := payload[key].(string); ok && strings.TrimSpace(value) != "" {
				return strings.TrimSpace(value)
			}
		}
		if rawErrors, ok := payload["errors"].([]any); ok {
			parts := make([]string, 0, len(rawErrors))
			for _, item := range rawErrors {
				text := strings.TrimSpace(fmt.Sprint(item))
				if text != "" {
					parts = append(parts, text)
				}
			}
			if len(parts) > 0 {
				return strings.Join(parts, "; ")
			}
		}
	}
	if text := strings.TrimSpace(string(body)); text != "" {
		return text
	}
	return fmt.Sprintf("HTTP %d", statusCode)
}
