// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

package fileoutput_test

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/realtimeinnovations/connext-cloud-cli/auth"
	"github.com/realtimeinnovations/connext-cloud-cli/config"
	"github.com/realtimeinnovations/connext-cloud-cli/gateway"
	"github.com/realtimeinnovations/connext-cloud-cli/internal/edgestore"
	"github.com/realtimeinnovations/connext-cloud-cli/spy"
)

// Verify production wiring with one shared invalid ancestor fixture. Do not
// repeat the utility's filesystem matrix for each consumer.
func TestManagedOutputCallersRejectInvalidAncestor(t *testing.T) {
	for _, tc := range []struct {
		name  string
		write func(string) error
	}{
		{"configuration", func(root string) error {
			return config.New(filepath.Join(root, "config.json")).WriteConfig(map[string]string{"region": "test"})
		}},
		{"token", func(root string) error {
			return auth.New(nil, filepath.Join(root, "token.json")).SaveAccessToken("dummy-token", 3600)
		}},
		{"gateway config", func(root string) error {
			return gateway.NewGatewayApp(root, io.Discard).WriteConfig(map[string]any{"test": true})
		}},
		{"gateway state", func(root string) error {
			return gateway.NewGatewayApp(root, io.Discard).WriteRuntimeState(map[string]any{"test": true})
		}},
		{"spy config", func(root string) error { return spy.NewApp(root, io.Discard).WriteConfig(map[string]any{"test": true}) }},
		{"spy state", func(root string) error {
			return spy.NewApp(root, io.Discard).WriteRuntimeState(map[string]any{"test": true})
		}},
		{"enrollment", func(root string) error {
			return edgestore.New(root).WriteEnrollArtifacts("service", "0:domain", "participant", "node", edgestore.EnrollArtifacts{DeviceCertPEM: []byte("cert"), PrivateKeyPEM: []byte("key")})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			outside := t.TempDir()
			link := filepath.Join(t.TempDir(), "link")
			if err := os.Symlink(outside, link); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
			if err := tc.write(filepath.Join(link, "new-root")); !errors.Is(err, os.ErrInvalid) {
				t.Fatalf("want unsafe output error, got %v", err)
			}
			entries, err := os.ReadDir(outside)
			if err != nil || len(entries) != 0 {
				t.Fatalf("output escaped: %v, %v", entries, err)
			}
		})
	}
}

func TestProjectCredentialPreflightRunsBeforeRegistration(t *testing.T) {
	for _, kind := range []string{"gateway", "spy"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			configuration := map[string]any{"databus": "db", "templates": map[string]any{"gateway": "gw", "app": "app"}}
			get := func(string) (map[string]any, error) { return map[string]any{"secure": true}, nil }
			generate := func(string, string, string) ([]byte, string, error) {
				t.Fatal("invalid credential path generated a key")
				return nil, "", nil
			}
			post := func(string, map[string]any) (map[string]any, error) {
				t.Fatal("invalid credential path registered a client")
				return nil, nil
			}
			var target string
			var ensure func() error
			if kind == "gateway" {
				app := gateway.NewGatewayApp(root, io.Discard)
				app.GetResourceFunc = get
				app.GenerateCSRFunc = generate
				app.APIPost = post
				target = app.RoutingDir()
				ensure = func() error { _, _, err := app.EnsureSecureArtifacts(configuration); return err }
			} else {
				app := spy.NewApp(root, io.Discard)
				app.GetResourceFunc = get
				app.GenerateCSRFunc = generate
				app.APIPost = post
				target = app.AppDir()
				ensure = func() error { _, err := app.EnsureSecureArtifacts(configuration); return err }
			}
			if err := os.MkdirAll(filepath.Join(target, "client.key"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := ensure(); !errors.Is(err, os.ErrInvalid) {
				t.Fatalf("want unsafe credential output error, got %v", err)
			}
		})
	}
}
