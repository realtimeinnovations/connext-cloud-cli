package httputil

import (
	"encoding/json"
	"fmt"
	"strings"
)

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
