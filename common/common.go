package common

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

type UserError struct {
	Message string
}

func (err UserError) Error() string {
	return err.Message
}

type TemplateItem struct {
	Name string
	Kind string
}

func TemplateItems(resource map[string]any, expectedKind string) []TemplateItem {
	clients, ok := resource["clients"]
	if !ok {
		clients = resource["applications"]
	}
	results := make([]TemplateItem, 0)
	expected := map[string]bool{expectedKind: true}
	if expectedKind == "observability-collector" {
		expected["telemetry-service-collector"] = true
	}
	switch typed := clients.(type) {
	case map[string]any:
		for name, rawInfo := range typed {
			info, _ := rawInfo.(map[string]any)
			kind, _ := info["kind"].(string)
			if name != "" && expected[kind] {
				results = append(results, TemplateItem{Name: name, Kind: kind})
			}
		}
	case []any:
		for _, raw := range typed {
			entry, _ := raw.(map[string]any)
			name, _ := entry["name"].(string)
			kind, _ := entry["kind"].(string)
			if name != "" && expected[kind] {
				results = append(results, TemplateItem{Name: name, Kind: kind})
			}
		}
	}
	sort.Slice(results, func(i, j int) bool { return results[i].Name < results[j].Name })
	return results
}

func ProjectID(value string, fallback string) string {
	if value == "" {
		cwd, _ := os.Getwd()
		value = filepath.Base(cwd)
	}
	value = regexp.MustCompile(`[^a-zA-Z0-9_.-]+`).ReplaceAllString(value, "-")
	value = strings.Trim(strings.ToLower(value), "-")
	if value == "" {
		return fallback
	}
	return value
}

func StringValue(config map[string]any, key string) string {
	if config == nil {
		return ""
	}
	value, _ := config[key].(string)
	return value
}

func NestedString(config map[string]any, keys ...string) string {
	current := any(config)
	for _, key := range keys {
		asMap, ok := current.(map[string]any)
		if !ok {
			return ""
		}
		current = asMap[key]
	}
	value, _ := current.(string)
	return value
}
