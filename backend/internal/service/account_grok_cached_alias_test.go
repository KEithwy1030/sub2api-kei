package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGrokExplicitMappingDoesNotAddCachedAlias(t *testing.T) {
	account := &Account{
		Platform: PlatformGrok,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"model_mapping": map[string]any{"grok-4.5": "grok-4.5"},
		},
	}

	require.False(t, account.IsModelSupported("grok-4.5-cached"))
}
