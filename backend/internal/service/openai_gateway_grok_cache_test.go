package service

import (
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestApplyGrokFreeResponsesPromptCacheRoute_FunctionTools(t *testing.T) {
	limit := int64(2_000_000)
	account := &Account{
		Platform: PlatformGrok,
		Type:     AccountTypeOAuth,
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
