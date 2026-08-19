package configaccess

import (
	"context"
	"net/http"
	"testing"

	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
)

func TestProviderRevalidatesDelegatedConfigKey(t *testing.T) {
	provider := newProvider("", []string{"config-key"})
	previous := &sdkaccess.Result{Provider: provider.Identifier(), Principal: "config-key"}

	result, authErr := provider.Revalidate(context.Background(), previous)
	if authErr != nil || result == nil || result.Principal != "config-key" {
		t.Fatalf("Revalidate() = %#v, %#v", result, authErr)
	}

	delete(provider.keys, "config-key")
	result, authErr = provider.Revalidate(context.Background(), previous)
	if result != nil || authErr == nil || authErr.HTTPStatusCode() != http.StatusUnauthorized {
		t.Fatalf("Revalidate(removed) = %#v, %#v; want HTTP 401", result, authErr)
	}
}
