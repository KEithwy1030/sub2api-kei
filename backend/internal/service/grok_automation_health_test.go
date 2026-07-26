package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type automatedGrokHealthCASRepoStub struct {
	casCalls       int
	updateCalls    int
	lastObservedAt time.Time
}

func (r *automatedGrokHealthCASRepoStub) UpdateExtra(context.Context, int64, map[string]any) error {
	r.updateCalls++
	return nil
}

func (r *automatedGrokHealthCASRepoStub) UpdateAutomatedGrokHealthIfNewer(
	_ context.Context,
	_ int64,
	observedAt time.Time,
	_ map[string]any,
) (bool, error) {
	r.casCalls++
	r.lastObservedAt = observedAt
	return true, nil
}

func managedGrokHealthTestAccount() *Account {
	return &Account{
		Platform:    PlatformGrok,
		Type:        AccountTypeOAuth,
		Status:      StatusActive,
		Schedulable: true,
		Extra: map[string]any{
			automatedGrokCleanupSourceKey: automatedGrokCleanupSourceValue,
			automatedGrokCleanupPolicyKey: automatedGrokCleanupPolicyValue,
		},
	}
}

func TestAutomatedGrokHealthAllowsSchedulingRequiresFreshSuccess(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	account := managedGrokHealthTestAccount()
	require.False(t, automatedGrokHealthAllowsScheduling(account, now), "unknown accounts must fail closed")

	account.Extra[automatedGrokHealthStateKey] = automatedGrokHealthHealthy
	account.Extra[automatedGrokHealthLastSuccessAtKey] = now.Add(-time.Hour).Format(time.RFC3339)
	require.True(t, automatedGrokHealthAllowsScheduling(account, now))

	account.Extra[automatedGrokHealthLastSuccessAtKey] = now.Add(-automatedGrokHealthSuccessTTL - time.Second).Format(time.RFC3339)
	require.False(t, automatedGrokHealthAllowsScheduling(account, now), "stale success must leave the production pool")
}

func TestAutomatedGrokHealthAllowsOnlyBoundedTransientRecovery(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	account := managedGrokHealthTestAccount()
	account.Extra[automatedGrokHealthStateKey] = automatedGrokHealthUnhealthy
	account.Extra[automatedGrokHealthLastSuccessAtKey] = now.Add(-time.Hour).Format(time.RFC3339)

	account.Extra[automatedGrokHealthFailureClassKey] = string(automatedGrokFailureRateLimited)
	account.Extra[automatedGrokHealthLastFailureAtKey] = now.Add(-automatedGrokHealthRateLimitCooldown + time.Second).Format(time.RFC3339)
	require.False(t, automatedGrokHealthAllowsScheduling(account, now))
	account.Extra[automatedGrokHealthLastFailureAtKey] = now.Add(-automatedGrokHealthRateLimitCooldown).Format(time.RFC3339)
	require.True(t, automatedGrokHealthAllowsScheduling(account, now))

	account.Extra[automatedGrokHealthFailureClassKey] = string(automatedGrokFailureUpstream)
	account.Extra[automatedGrokHealthLastFailureAtKey] = now.Add(-automatedGrokHealthTransientCooldown).Format(time.RFC3339)
	require.True(t, automatedGrokHealthAllowsScheduling(account, now))

	for _, class := range []automatedGrokFailureClass{
		automatedGrokFailureFreeUsageExhausted,
		automatedGrokFailureQuotaExhausted,
		automatedGrokFailurePermissionDenied,
		automatedGrokFailureCredential,
		automatedGrokFailureForbidden,
		automatedGrokFailureUnknown,
	} {
		account.Extra[automatedGrokHealthFailureClassKey] = string(class)
		require.False(t, automatedGrokHealthAllowsScheduling(account, now), "class %s must require a successful retest", class)
	}
}

func TestAutomatedGrokHealthDoesNotGateUnmanagedAccounts(t *testing.T) {
	account := managedGrokHealthTestAccount()
	delete(account.Extra, automatedGrokCleanupPolicyKey)
	require.True(t, automatedGrokHealthAllowsScheduling(account, time.Now()))
}

func TestClassifyAutomatedGrokScheduledFailure(t *testing.T) {
	tests := []struct {
		name    string
		message string
		want    automatedGrokFailureClass
	}{
		{name: "permission", message: permanentGrokTestError(), want: automatedGrokFailurePermissionDenied},
		{name: "quota", message: `Grok Responses API returned 402: {"error":"payment required"}`, want: automatedGrokFailureQuotaExhausted},
		{name: "free usage exhausted", message: `Grok Responses API returned 429: {"code":"subscription:free-usage-exhausted","error":"rolling 24-hour window"}`, want: automatedGrokFailureFreeUsageExhausted},
		{name: "rate limit", message: `Grok Responses API returned 429: {"error":"rate limit"}`, want: automatedGrokFailureRateLimited},
		{name: "generic forbidden", message: `Grok Responses API returned 403: {"error":"forbidden"}`, want: automatedGrokFailureForbidden},
		{name: "upstream", message: "Grok Responses API returned 503: unavailable", want: automatedGrokFailureUpstream},
		{name: "expired", message: "Failed to get Grok access token: grok oauth access token is expired", want: automatedGrokFailureCredential},
		{name: "transport", message: "request failed: connection reset by peer", want: automatedGrokFailureTransport},
		{name: "unknown", message: "unexpected response", want: automatedGrokFailureUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, classifyAutomatedGrokScheduledFailure(tt.message))
		})
	}
}

func TestIsGrokFreeUsageExhaustedResponseIsStrict(t *testing.T) {
	require.True(t, isGrokFreeUsageExhaustedResponse(429, []byte(`{"code":"subscription:free-usage-exhausted","error":"rolling 24-hour window"}`)))
	require.False(t, isGrokFreeUsageExhaustedResponse(429, []byte(`{"code":"rate_limited","error":"subscription:free-usage-exhausted"}`)))
	require.False(t, isGrokFreeUsageExhaustedResponse(429, []byte(`subscription:free-usage-exhausted`)))
	require.False(t, isGrokFreeUsageExhaustedResponse(503, []byte(`{"code":"subscription:free-usage-exhausted"}`)))
}

func TestIsPermanentGrokPermissionDeniedResponseIsStrict(t *testing.T) {
	body := []byte(`{"code":"permission-denied","error":"Access to the chat endpoint is denied. Please ensure you're using the correct credentials. If you believe this is a mistake, please contact support."}`)
	require.True(t, isPermanentGrokPermissionDeniedResponse(403, body))
	require.False(t, isPermanentGrokPermissionDeniedResponse(429, body))
	require.False(t, isPermanentGrokPermissionDeniedResponse(403, []byte(`{"code":"permission-denied","error":"subscription required"}`)))
}

func TestPersistAutomatedGrokHealthUsesObservationCAS(t *testing.T) {
	account := managedGrokHealthTestAccount()
	account.ID = 99
	repo := &automatedGrokHealthCASRepoStub{}
	observedAt := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, persistAutomatedGrokHealthFailure(
		context.Background(),
		repo,
		account,
		automatedGrokFailureFreeUsageExhausted,
		observedAt,
	))
	require.Equal(t, 1, repo.casCalls)
	require.Zero(t, repo.updateCalls)
	require.Equal(t, observedAt, repo.lastObservedAt)
}
