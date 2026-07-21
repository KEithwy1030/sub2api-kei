package service

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func permanentGrokTestError() string {
	return `Grok Responses API returned 403: {"code":"permission-denied","error":"Access to the chat endpoint is denied. Please ensure you're using the correct credentials. If you believe this is a mistake, please log into console.x.ai and update the permissions, or contact support."}`
}

func TestIsPermanentGrokScheduledTestFailureIsStrict(t *testing.T) {
	tests := []struct {
		name string
		text string
		want bool
	}{
		{name: "observed permission denial", text: permanentGrokTestError(), want: true},
		{name: "underscore spelling", text: `Grok Responses API returned 403: {"code":"permission_denied","error":"Access to the chat endpoint is denied. Please ensure you're using the correct credentials. If you believe this is a mistake, please contact support."}`, want: true},
		{name: "rate limit", text: `Grok Responses API returned 429: {"code":"rate_limited"}`},
		{name: "generic forbidden", text: `Grok Responses API returned 403: {"error":"Access denied"}`},
		{name: "subscription wording", text: `Grok Responses API returned 403: {"code":"permission_denied","error":"Access to the chat endpoint is denied because a subscription is required"}`},
		{name: "provider outage", text: `Grok Responses API returned 503: unavailable`},
		{name: "malformed", text: `Grok Responses API returned 403: permission_denied`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, isPermanentGrokScheduledTestFailure(tt.text))
		})
	}
}

func TestHasStablePermanentGrokFailuresRequiresThreeAcrossElevenHours(t *testing.T) {
	now := time.Now()
	makeResult := func(hoursAgo int, status, message string) *ScheduledTestResult {
		started := now.Add(-time.Duration(hoursAgo) * time.Hour)
		return &ScheduledTestResult{Status: status, ErrorMessage: message, StartedAt: started, FinishedAt: started.Add(time.Minute)}
	}
	stable := []*ScheduledTestResult{
		makeResult(0, "failed", permanentGrokTestError()),
		makeResult(6, "failed", permanentGrokTestError()),
		makeResult(12, "failed", permanentGrokTestError()),
	}
	require.True(t, hasStablePermanentGrokFailures(stable))
	require.False(t, hasStablePermanentGrokFailures(stable[:2]))

	withSuccess := append([]*ScheduledTestResult(nil), stable...)
	withSuccess[1] = makeResult(6, "success", "")
	require.False(t, hasStablePermanentGrokFailures(withSuccess))

	shortSpan := append([]*ScheduledTestResult(nil), stable...)
	shortSpan[2] = makeResult(10, "failed", permanentGrokTestError())
	require.False(t, hasStablePermanentGrokFailures(shortSpan))
}

func TestAutomatedGrokCleanupRequiresExplicitPolicyAndGrace(t *testing.T) {
	now := time.Now().UTC()
	account := &Account{
		Platform: PlatformGrok,
		Type:     AccountTypeOAuth,
		Extra: map[string]any{
			automatedGrokCleanupSourceKey:    automatedGrokCleanupSourceValue,
			automatedGrokCleanupPolicyKey:    automatedGrokCleanupPolicyValue,
			automatedGrokCleanupEnabledAtKey: now.Add(-25 * time.Hour).Format(time.RFC3339),
		},
	}
	require.True(t, automatedGrokCleanupEnabled(account, now))

	account.Extra[automatedGrokCleanupEnabledAtKey] = now.Add(-23 * time.Hour).Format(time.RFC3339)
	require.False(t, automatedGrokCleanupEnabled(account, now))
	account.Extra[automatedGrokCleanupEnabledAtKey] = now.Add(-25 * time.Hour).Format(time.RFC3339)
	account.Extra[automatedGrokCleanupSourceKey] = "manual"
	require.False(t, automatedGrokCleanupEnabled(account, now), fmt.Sprintf("unexpected cleanup eligibility: %#v", account.Extra))
}
