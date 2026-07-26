package service

import (
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
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

func TestHasStablePermanentGrokFailuresRequiresThreeAcrossTwentyFourHours(t *testing.T) {
	now := time.Now()
	makeResult := func(hoursAgo int, status, message string) *ScheduledTestResult {
		started := now.Add(-time.Duration(hoursAgo) * time.Hour)
		return &ScheduledTestResult{Status: status, ErrorMessage: message, StartedAt: started, FinishedAt: started.Add(time.Minute)}
	}
	stable := []*ScheduledTestResult{
		makeResult(0, "failed", permanentGrokTestError()),
		makeResult(12, "failed", permanentGrokTestError()),
		makeResult(24, "failed", permanentGrokTestError()),
	}
	require.True(t, hasStablePermanentGrokFailures(stable))
	require.False(t, hasStablePermanentGrokFailures(stable[:2]))

	withSuccess := append([]*ScheduledTestResult(nil), stable...)
	withSuccess[1] = makeResult(12, "success", "")
	require.False(t, hasStablePermanentGrokFailures(withSuccess))

	shortSpan := append([]*ScheduledTestResult(nil), stable...)
	shortSpan[2] = makeResult(23, "failed", permanentGrokTestError())
	require.False(t, hasStablePermanentGrokFailures(shortSpan))
}

func TestAutomatedGrokCleanupRequiresExplicitActivePolicy(t *testing.T) {
	now := time.Now().UTC()
	account := &Account{
		Platform:  PlatformGrok,
		Type:      AccountTypeOAuth,
		CreatedAt: now.Add(-25 * time.Hour),
		Extra: map[string]any{
			automatedGrokCleanupSourceKey:    automatedGrokCleanupSourceValue,
			automatedGrokCleanupPolicyKey:    automatedGrokCleanupPolicyValue,
			automatedGrokCleanupEnabledAtKey: now.Add(-time.Minute).Format(time.RFC3339),
		},
	}
	require.True(t, automatedGrokCleanupEnabled(account, now))

	account.CreatedAt = now.Add(-23 * time.Hour)
	require.False(t, automatedGrokCleanupEnabled(account, now))
	account.CreatedAt = now.Add(-25 * time.Hour)
	account.Extra[automatedGrokCleanupEnabledAtKey] = now.Add(time.Minute).Format(time.RFC3339)
	require.False(t, automatedGrokCleanupEnabled(account, now))
	account.Extra[automatedGrokCleanupEnabledAtKey] = now.Add(-time.Minute).Format(time.RFC3339)
	account.Extra[automatedGrokCleanupSourceKey] = "manual"
	require.False(t, automatedGrokCleanupEnabled(account, now), fmt.Sprintf("unexpected cleanup eligibility: %#v", account.Extra))
}

func TestPermanentAutomatedGrokCredentialStateIsStrict(t *testing.T) {
	tests := []struct {
		name    string
		status  string
		message string
		want    bool
	}{
		{name: "revoked refresh token", status: StatusError, message: `Token refresh failed (non-retryable): status 400 invalid_grant refresh token has been revoked`, want: true},
		{name: "missing refresh token", status: StatusError, message: "Grok OAuth credential reconciliation: missing refresh token", want: true},
		{name: "oauth 401 without refresh", status: StatusError, message: "Authentication failed (401): refresh_token missing, cannot recover", want: true},
		{name: "temporary refresh failure", status: StatusError, message: "Token refresh failed (non-retryable): provider timeout"},
		{name: "proxy configuration", status: StatusError, message: "Grok OAuth account proxy configuration is invalid"},
		{name: "active account", status: StatusActive, message: `Token refresh failed (non-retryable): invalid_grant`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, isPermanentAutomatedGrokCredentialState(&Account{Status: tt.status, ErrorMessage: tt.message}))
		})
	}
}

func TestShouldRetireAutomatedGrokDoesNotRequireLatestProbeFailureForPermanentCredentials(t *testing.T) {
	account := &Account{
		Status:       StatusError,
		ErrorMessage: `Token refresh failed (non-retryable): status 400 invalid_grant refresh token has been revoked`,
	}
	recentSuccess := &ScheduledTestResult{Status: "success"}

	require.True(t, shouldRetireAutomatedGrok(account, recentSuccess))
	require.False(t, shouldRetireAutomatedGrok(&Account{Status: StatusActive}, recentSuccess))
}

func TestHasStableGrokRecoveryRequiresTwoSuccessesAcrossTwelveHours(t *testing.T) {
	now := time.Now().UTC()
	stable := []*ScheduledTestResult{
		{Status: "success", StartedAt: now.Add(-time.Minute), FinishedAt: now},
		{Status: "success", StartedAt: now.Add(-13 * time.Hour), FinishedAt: now.Add(-13*time.Hour + time.Minute)},
	}
	require.True(t, hasStableGrokRecovery(stable))
	require.False(t, hasStableGrokRecovery(stable[:1]))

	failed := append([]*ScheduledTestResult(nil), stable...)
	failed[1] = &ScheduledTestResult{Status: "failed", StartedAt: now.Add(-13 * time.Hour), FinishedAt: now.Add(-13*time.Hour + time.Minute)}
	require.False(t, hasStableGrokRecovery(failed))

	tooClose := append([]*ScheduledTestResult(nil), stable...)
	tooClose[1] = &ScheduledTestResult{Status: "success", StartedAt: now.Add(-11 * time.Hour), FinishedAt: now.Add(-11*time.Hour + time.Minute)}
	require.False(t, hasStableGrokRecovery(tooClose))
}

func TestAutomatedGrokProbeCronsAreDeterministicAndStaggered(t *testing.T) {
	require.Equal(t, automatedGrokColdPoolCron(42), automatedGrokColdPoolCron(42))
	require.NotEqual(t, automatedGrokColdPoolCron(42), automatedGrokColdPoolCron(43))
	require.Equal(t, automatedGrokActiveProbeCron(42), automatedGrokActiveProbeCron(42))
	require.Contains(t, automatedGrokActiveProbeCron(42), ",")
}

func TestScheduledProbeRecoversActiveGrokSlowTTFTQuarantineOnlyWhenFast(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.OpenAIScheduler.GrokSlowQuarantineTTFTMs = 20_000
	runner := &ScheduledTestRunnerService{cfg: cfg, rateLimitSvc: &RateLimitService{}}
	account := &Account{
		ID:       44,
		Platform: PlatformGrok,
		Type:     AccountTypeOAuth,
		Extra: map[string]any{
			modelRateLimitsKey: map[string]any{
				"grok-4.5": map[string]any{
					"rate_limit_reset_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
					"reason":              "grok slow ttft quarantine: 25000ms (2 consecutive)",
				},
			},
		},
	}

	require.True(t, runner.shouldRecoverGrokSlowTTFTFromProbe(account, &ScheduledTestResult{Status: "success", LatencyMs: 12_000}))
	require.False(t, runner.shouldRecoverGrokSlowTTFTFromProbe(account, &ScheduledTestResult{Status: "success", LatencyMs: 25_000}))
	require.False(t, runner.shouldRecoverGrokSlowTTFTFromProbe(account, &ScheduledTestResult{Status: "failed", LatencyMs: 1_000}))
}
