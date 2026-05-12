package app

import (
	"errors"
	"testing"

	"github.com/realtimeinnovations/connext-cloud-cli/config"
)

func TestDecodeGatewayJSONPassesThroughNotConfiguredError(t *testing.T) {
	_, err := decodeCommandJSON(nil, config.ErrNotConfigured, "GET", "/databuses?extra_fields=true", "", "gateway")
	if err == nil {
		t.Fatal("expected error")
	}
	if err.Error() != config.NotConfiguredMessage {
		t.Fatalf("unexpected error message: %s", err)
	}
	if !errors.Is(config.ErrNotConfigured, config.ErrNotConfigured) {
		t.Fatal("expected sentinel error")
	}
}
