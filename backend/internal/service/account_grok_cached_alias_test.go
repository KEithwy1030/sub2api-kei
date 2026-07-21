package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGrokExplicitMappingKeepsCachedCompatibilityAlias(t *testing.T) {
	account := &Account{
		Platform: PlatformGrok,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"model_mapping": map[string]any{"grok-4.5": "grok-4.5"},
		},
	}

	require.True(t, account.IsModelSupported("grok-4.5-cached"))
	require.Equal(t, "grok-4.5", account.GetMappedModel("grok-4.5-cached"))
}

func TestGrokCachedCompatibilityAliasDoesNotBypassExplicitWhitelist(t *testing.T) {
	account := &Account{
		Platform: PlatformGrok,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"model_mapping": map[string]any{"grok-4.3": "grok-4.3"},
		},
	}

	require.False(t, account.IsModelSupported("grok-4.5-cached"))
}
