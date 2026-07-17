package service

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestApplyGrokFreeResponsesPromptCacheRoute_FunctionTools(t *testing.T) {
	limit := int64(2_000_000)
	account := &Account{
		Platform:    PlatformGrok,
		Type:        AccountTypeOAuth,
		Status:      StatusActive,
		Schedulable: true,
		Extra: map[string]any{
			grokQuotaSnapshotExtraKey: xai.QuotaSnapshot{
				Tokens: &xai.QuotaWindow{Limit: &limit},
			},
		},
	}
	body := []byte(`{"model":"grok-4.5","prompt_cache_key":"session-1","tools":[{"type":"function","name":"shell","description":"run"},{"type":"function","name":"web_search","description":"search"}],"tool_choice":"auto"}`)

	got, err := applyGrokFreeResponsesPromptCacheRoute(body, body, account, 12, "grok-4.5")
	require.NoError(t, err)
	require.NotEqual(t, "session-1", gjson.GetBytes(got, "prompt_cache_key").String())
	require.Len(t, gjson.GetBytes(got, "tools").Array(), 3)
	require.Equal(t, "function", gjson.GetBytes(got, "tools.0.type").String())
	require.Equal(t, "web_search", gjson.GetBytes(got, "tools.1.type").String())
	require.Equal(t, "x_search", gjson.GetBytes(got, "tools.2.type").String())
	headers := make(http.Header)
	applyGrokFreePromptCacheHeader(headers, got, account)
	require.Equal(t, gjson.GetBytes(got, "prompt_cache_key").String(), headers.Get(grokPromptCacheHeader))
}

func TestApplyGrokFreeResponsesPromptCacheRoute_ToolFree(t *testing.T) {
	account := &Account{
		Platform:    PlatformGrok,
		Type:        AccountTypeOAuth,
		Credentials: map[string]any{"subscription_tier": "free"},
	}
	body := []byte(`{"model":"grok-4.5","prompt_cache_key":"session-1","input":"hello"}`)

	got, err := applyGrokFreeResponsesPromptCacheRoute(body, body, account, 12, "grok-4.5")
	require.NoError(t, err)
	require.Equal(t, "none", gjson.GetBytes(got, "tool_choice").String())
	require.Equal(t, "web_search", gjson.GetBytes(got, "tools.0.type").String())
	require.Equal(t, "x_search", gjson.GetBytes(got, "tools.1.type").String())
}

func TestApplyGrokFreeResponsesPromptCacheRoute_PaidAccountUnchanged(t *testing.T) {
	limit := int64(2_000_000)
	account := &Account{
		Platform:    PlatformGrok,
		Type:        AccountTypeOAuth,
		Credentials: map[string]any{"subscription_tier": "SuperGrok"},
		Extra: map[string]any{
			grokQuotaSnapshotExtraKey: xai.QuotaSnapshot{
				Tokens: &xai.QuotaWindow{Limit: &limit},
			},
		},
	}
	body := []byte(`{"model":"grok-4.5","prompt_cache_key":"session-1","tools":[{"type":"function","name":"shell"}]}`)

	got, err := applyGrokFreeResponsesPromptCacheRoute(body, body, account, 12, "grok-4.5")
	require.NoError(t, err)
	require.JSONEq(t, string(body), string(got))
}

func TestApplyGrokFreeResponsesPromptCacheRoute_RequiresCacheKey(t *testing.T) {
	account := &Account{
		Platform:    PlatformGrok,
		Type:        AccountTypeOAuth,
		Credentials: map[string]any{"subscription_tier": "free"},
	}
	body := []byte(`{"model":"grok-4.5","tools":[{"type":"function","name":"shell"}]}`)

	got, err := applyGrokFreeResponsesPromptCacheRoute(body, body, account, 12, "grok-4.5")
	require.NoError(t, err)
	require.JSONEq(t, string(body), string(got))
}

func TestGrokPromptCacheProbeUpdates_StateMachine(t *testing.T) {
	now := time.Date(2026, 7, 18, 10, 0, 0, 0, time.UTC)
	account := &Account{
		Platform:    PlatformGrok,
		Type:        AccountTypeOAuth,
		Credentials: map[string]any{"subscription_tier": "free"},
		Extra:       map[string]any{},
	}

	for failure := 1; failure <= grokPromptCacheUnsupportedAfter; failure++ {
		updates := grokPromptCacheProbeUpdates(account, grokPromptCacheProbeOutcome{HTTPStatus: http.StatusOK}, now)
		mergeAccountExtra(account, updates)
		if failure < grokPromptCacheUnsupportedAfter {
			require.Equal(t, grokPromptCacheStateUnknown, grokPromptCacheState(account))
		} else {
			require.Equal(t, grokPromptCacheStateUnsupported, grokPromptCacheState(account))
		}
	}

	updates := grokPromptCacheProbeUpdates(account, grokPromptCacheProbeOutcome{
		HTTPStatus:      http.StatusOK,
		CacheReadTokens: 1024,
	}, now.Add(time.Hour))
	mergeAccountExtra(account, updates)
	require.Equal(t, grokPromptCacheStateSupported, grokPromptCacheState(account))
	require.Zero(t, grokPromptCacheExtraInt(account, grokPromptCacheFailuresExtraKey))

	for failure := 1; failure < grokPromptCacheUnsupportedAfter; failure++ {
		updates = grokPromptCacheProbeUpdates(account, grokPromptCacheProbeOutcome{HTTPStatus: http.StatusOK}, now.Add(time.Duration(failure+1)*time.Hour))
		mergeAccountExtra(account, updates)
		require.Equal(t, grokPromptCacheStateSupported, grokPromptCacheState(account))
	}
	updates = grokPromptCacheProbeUpdates(account, grokPromptCacheProbeOutcome{HTTPStatus: http.StatusOK}, now.Add(4*time.Hour))
	mergeAccountExtra(account, updates)
	require.Equal(t, grokPromptCacheStateUnsupported, grokPromptCacheState(account))
}

func TestGrokPromptCacheProbeUpdates_TransientResponsePreservesState(t *testing.T) {
	account := &Account{
		Platform:    PlatformGrok,
		Type:        AccountTypeOAuth,
		Credentials: map[string]any{"subscription_tier": "free"},
		Extra: map[string]any{
			grokPromptCacheStateExtraKey: grokPromptCacheStateSupported,
		},
	}
	for _, status := range []int{http.StatusForbidden, http.StatusTooManyRequests, http.StatusBadGateway} {
		require.Nil(t, grokPromptCacheProbeUpdates(account, grokPromptCacheProbeOutcome{HTTPStatus: status}, time.Now()))
		require.Equal(t, grokPromptCacheStateSupported, grokPromptCacheState(account))
	}
}

func TestGrokPromptCacheAliasRequiresVerificationForFreeAccount(t *testing.T) {
	account := &Account{
		Platform:    PlatformGrok,
		Type:        AccountTypeOAuth,
		Status:      StatusActive,
		Schedulable: true,
		Credentials: map[string]any{
			"subscription_tier": "free",
			"model_mapping":     map[string]any{"grok-4.5": "grok-4.5"},
		},
		Extra: map[string]any{},
	}
	service := &GatewayService{}

	require.False(t, service.isModelSupportedByAccount(account, grokPromptCacheModelAlias))
	account.Extra[grokPromptCacheStateExtraKey] = grokPromptCacheStateSupported
	require.True(t, account.IsModelSupported(grokPromptCacheModelAlias))
	require.True(t, service.isModelSupportedByAccount(account, grokPromptCacheModelAlias))
	require.True(t, isOpenAICompatibleAccountEligibleForRequest(context.Background(), account, PlatformGrok, grokPromptCacheModelAlias, false, ""))
	require.True(t, (&defaultOpenAIAccountScheduler{}).isAccountRequestCompatible(context.Background(), account, OpenAIAccountScheduleRequest{RequestedModel: grokPromptCacheModelAlias}))
	require.Equal(t, "grok-4.5", resolveGrokUpstreamModel(account, grokPromptCacheModelAlias))
}

func TestGrokPromptCacheAliasKeepsExplicitPaidFallback(t *testing.T) {
	account := &Account{
		Platform:    PlatformGrok,
		Type:        AccountTypeOAuth,
		Status:      StatusActive,
		Schedulable: true,
		Credentials: map[string]any{
			"subscription_tier": "SuperGrok",
			"model_mapping": map[string]any{
				grokPromptCacheModelAlias: "grok-4.5",
			},
		},
	}
	service := &GatewayService{}
	require.True(t, account.IsModelSupported(grokPromptCacheModelAlias))
	require.True(t, service.isModelSupportedByAccount(account, grokPromptCacheModelAlias))
	require.True(t, isOpenAICompatibleAccountEligibleForRequest(context.Background(), account, PlatformGrok, grokPromptCacheModelAlias, false, ""))
	require.Equal(t, "grok-4.5", resolveGrokUpstreamModel(account, grokPromptCacheModelAlias))
}

func TestGrokPromptCacheTokensFromResponse(t *testing.T) {
	body := []byte("data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens_details\":{\"cached_tokens\":2048}}}}\n\n")
	require.Equal(t, 2048, grokPromptCacheTokensFromResponse(body))
	require.Equal(t, 512, grokPromptCacheTokensFromResponse([]byte(`{"usage":{"prompt_tokens_details":{"cached_tokens":512}}}`)))
}
