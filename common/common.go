package common

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
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

// GenerateClientID returns a unique client identifier in the form "cli-<16hex>".
// The identifier is kept short so that the X.509 CN value built by the server
// as "{databus}.{template}.{clientID}" stays within the 64-character RFC 5280 limit.
func GenerateClientID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("cli-%x", b)
}

var SecureFiles = []string{
	"client.key",
	"client.crt",
	"identity_ca.crt",
	"permissions_ca.crt",
	"signed_governance.p7s",
	"signed_permissions.p7s",
	"psk.key",
}

func FileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func IsSecure(resource map[string]any) bool {
	if resource == nil {
		return false
	}
	config, _ := resource["config"].(map[string]any)
	if secure, ok := config["secure"].(bool); ok && secure {
		return true
	}
	if secure, ok := resource["secure"].(bool); ok {
		return secure
	}
	return false
}

func LocalSecureFilesExist(directory string) bool {
	for _, name := range SecureFiles {
		if !FileExists(filepath.Join(directory, name)) {
			return false
		}
	}
	return true
}

func SaveSecureFiles(files map[string]string, privateKey []byte, targetDir string) error {
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return err
	}
	for filename, content := range files {
		decoded, err := base64.StdEncoding.DecodeString(content)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(targetDir, filename), decoded, secureFileMode(filename)); err != nil {
			return err
		}
	}
	if len(privateKey) > 0 {
		if err := os.WriteFile(filepath.Join(targetDir, "client.key"), privateKey, secureFileMode("client.key")); err != nil {
			return err
		}
	}
	return nil
}

func secureFileMode(fileName string) os.FileMode {
	if strings.HasSuffix(fileName, ".key") {
		return 0o600
	}
	return 0o644
}

func TemplateListContains(items []TemplateItem, target string) bool {
	for _, item := range items {
		if item.Name == target {
			return true
		}
	}
	return false
}

func SortedKeys(values map[string]map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
