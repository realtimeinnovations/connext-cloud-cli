// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

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

	"github.com/realtimeinnovations/connext-cloud-cli/internal/fileoutput"
)

type UserError struct {
	Message string
	Cause   error
}

func (err UserError) Error() string {
	return err.Message
}

func (err UserError) Unwrap() error { return err.Cause }

// StaleConfigError is retained for compatibility with the existing Gateway
// validation path. Reset-hint policy is determined at the API boundary.
type StaleConfigError struct {
	Err error
}

func (err StaleConfigError) Error() string {
	return err.Err.Error()
}

func (err StaleConfigError) Unwrap() error {
	return err.Err
}

type TemplateItem struct {
	Name string
	Kind string
}

const (
	ServiceStatusUnknown  = "unknown"
	ServiceStatusCreating = "creating"
	ServiceStatusActive   = "active"
	ServiceStatusDeleting = "deleting"
	ServiceStatusDisabled = "disabled"
	ServiceStatusError    = "error"
)

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
	"governance.p7s",
	"permissions.p7s",
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

func RemoveSecureFiles(directory string) error {
	for _, name := range SecureFiles {
		path := filepath.Join(directory, name)
		err := os.Remove(path)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

// ValidateSecureFileNames keeps server-supplied credentials inside their directory.
func ValidateSecureFileNames(files map[string]string) error {
	for name := range files {
		if !fileoutput.ValidName(name) {
			return fmt.Errorf("invalid secure filename %q: expected a filename without path components", name)
		}
	}
	return nil
}

// DecodeSecureFiles validates the entire response before any output is written.
// A locally generated private key takes precedence over all case variants of
// the server's copy, including on case-insensitive filesystems.
func DecodeSecureFiles(files map[string]string, privateKey []byte) (map[string][]byte, error) {
	if err := ValidateSecureFileNames(files); err != nil {
		return nil, err
	}
	decoded := make(map[string][]byte, len(files)+1)
	for name, content := range files {
		if len(privateKey) > 0 && strings.EqualFold(name, "client.key") {
			continue
		}
		data, err := base64.StdEncoding.DecodeString(content)
		if err != nil {
			return nil, fmt.Errorf("decode %s: %w", name, err)
		}
		decoded[name] = data
	}
	if len(privateKey) > 0 {
		decoded["client.key"] = privateKey
	}
	return decoded, nil
}

// PreflightSecureDirectory checks existing entries before requesting credentials,
// whose complete filenames are only known after the response arrives.
func PreflightSecureDirectory(directory string) error {
	if err := fileoutput.Validate(directory, fileoutput.Directory); err != nil {
		return err
	}
	entries, err := os.ReadDir(directory)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := fileoutput.Validate(filepath.Join(directory, entry.Name()), fileoutput.File); err != nil {
			return err
		}
	}
	return nil
}

func SaveSecureFiles(files map[string]string, privateKey []byte, targetDir string) error {
	decoded, err := DecodeSecureFiles(files, privateKey)
	if err != nil {
		return err
	}
	for name := range decoded {
		if err := fileoutput.ValidateWithin(filepath.Clean(targetDir), filepath.Join(targetDir, name), fileoutput.File); err != nil {
			return err
		}
	}
	if err := fileoutput.MkdirAll(targetDir, 0o755); err != nil {
		return err
	}
	for name, data := range decoded {
		if err := fileoutput.WriteFile(filepath.Join(targetDir, name), data, secureFileMode(name)); err != nil {
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
