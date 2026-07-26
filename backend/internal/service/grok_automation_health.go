package service

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

const (
	automatedGrokHealthStateKey         = "automation_health_state"
	automatedGrokHealthLastSuccessAtKey = "automation_health_last_success_at"
	automatedGrokHealthLastFailureAtKey = "automation_health_last_failure_at"
	automatedGrokHealthFailureClassKey  = "automation_health_failure_class"
	automatedGrokHealthObservedAtKey    = "automation_health_observed_at"

	automatedGrokHealthHealthy   = "healthy"
	automatedGrokHealthUnhealthy = "unhealthy"

	automatedGrokHealthSuccessTTL        = 18 * time.Hour
	automatedGrokHealthRateLimitCooldown = 30 * time.Minute
	automatedGrokHealthTransientCooldown = 10 * time.Minute
	automatedGrokFreeUsageSafetyCooldown = 24 * time.Hour
)

type automatedGrokFailureClass string

const (
	automatedGrokFailurePermissionDenied   automatedGrokFailureClass = "permission_denied"
	automatedGrokFailureFreeUsageExhausted automatedGrokFailureClass = "free_usage_exhausted"
	automatedGrokFailureQuotaExhausted     automatedGrokFailureClass = "quota_exhausted"
	automatedGrokFailureRateLimited        automatedGrokFailureClass = "rate_limited"
	automatedGrokFailureCredential         automatedGrokFailureClass = "credential_refresh_pending"
	automatedGrokFailureForbidden          automatedGrokFailureClass = "forbidden_unknown"
	automatedGrokFailureUpstream           automatedGrokFailureClass = "transient_upstream"
	automatedGrokFailureTransport          automatedGrokFailureClass = "transient_transport"
	automatedGrokFailureUnknown            automatedGrokFailureClass = "unknown"
)

type automatedGrokHealthRepository interface {
	UpdateExtra(ctx context.Context, id int64, updates map[string]any) error
}

type automatedGrokHealthCASRepository interface {
	UpdateAutomatedGrokHealthIfNewer(ctx context.Context, id int64, observedAt time.Time, updates map[string]any) (bool, error)
}

type grokUpstreamErrorEnvelope struct {
	Code  string `json:"code"`
	Error string `json:"error"`
}

func isGrokFreeUsageExhaustedResponse(statusCode int, responseBody []byte) bool {
	if statusCode != 429 || len(responseBody) == 0 {
		return false
	}
	var envelope grokUpstreamErrorEnvelope
	if err := json.Unmarshal(responseBody, &envelope); err != nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(envelope.Code), "subscription:free-usage-exhausted")
}

func isPermanentGrokPermissionDeniedResponse(statusCode int, responseBody []byte) bool {
	if statusCode != 403 || len(responseBody) == 0 {
		return false
	}
	var envelope grokUpstreamErrorEnvelope
	if err := json.Unmarshal(responseBody, &envelope); err != nil || normalizeScheduledGrokErrorCode(envelope.Code) != "permission_denied" {
		return false
	}
	const deniedPrefix = "access to the chat endpoint is denied. please ensure you're using the correct credentials. if you believe this is a mistake, please"
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(envelope.Error)), deniedPrefix)
}

func automatedGrokHealthFailureUpdates(class automatedGrokFailureClass, observedAt time.Time) map[string]any {
	return map[string]any{
		automatedGrokHealthStateKey:         automatedGrokHealthUnhealthy,
		automatedGrokHealthLastFailureAtKey: observedAt.UTC().Format(time.RFC3339),
		automatedGrokHealthFailureClassKey:  string(class),
		automatedGrokHealthObservedAtKey:    observedAt.UTC().Format(time.RFC3339Nano),
	}
}

func automatedGrokHealthSuccessUpdates(observedAt time.Time) map[string]any {
	return map[string]any{
		automatedGrokHealthStateKey:         automatedGrokHealthHealthy,
		automatedGrokHealthLastSuccessAtKey: observedAt.UTC().Format(time.RFC3339),
		automatedGrokHealthFailureClassKey:  nil,
		automatedGrokHealthLastFailureAtKey: nil,
		automatedGrokHealthObservedAtKey:    observedAt.UTC().Format(time.RFC3339Nano),
	}
}

func persistAutomatedGrokHealthUpdates(
	ctx context.Context,
	repo automatedGrokHealthRepository,
	account *Account,
	observedAt time.Time,
	updates map[string]any,
) (bool, error) {
	if repo == nil || !automatedGrokHealthManaged(account) {
		return false, nil
	}
	if casRepo, ok := repo.(automatedGrokHealthCASRepository); ok {
		return casRepo.UpdateAutomatedGrokHealthIfNewer(ctx, account.ID, observedAt, updates)
	}
	if err := repo.UpdateExtra(ctx, account.ID, updates); err != nil {
		return false, err
	}
	return true, nil
}

func persistAutomatedGrokHealthFailure(
	ctx context.Context,
	repo automatedGrokHealthRepository,
	account *Account,
	class automatedGrokFailureClass,
	observedAt time.Time,
) error {
	if repo == nil || !automatedGrokHealthManaged(account) {
		return nil
	}
	_, err := persistAutomatedGrokHealthUpdates(
		ctx,
		repo,
		account,
		observedAt,
		automatedGrokHealthFailureUpdates(class, observedAt),
	)
	return err
}

func automatedGrokHealthManaged(account *Account) bool {
	if account == nil || !account.IsGrokOAuth() || account.Extra == nil {
		return false
	}
	return strings.TrimSpace(anyString(account.Extra[automatedGrokCleanupSourceKey])) == automatedGrokCleanupSourceValue &&
		strings.TrimSpace(anyString(account.Extra[automatedGrokCleanupPolicyKey])) == automatedGrokCleanupPolicyValue
}

func automatedGrokHealthAllowsScheduling(account *Account, now time.Time) bool {
	if !automatedGrokHealthManaged(account) {
		return true
	}
	if !automatedGrokHasFreshSuccess(account, now) {
		return false
	}

	state := strings.TrimSpace(anyString(account.Extra[automatedGrokHealthStateKey]))
	if state == automatedGrokHealthHealthy {
		return true
	}
	if state != automatedGrokHealthUnhealthy {
		return false
	}

	failedAt, ok := parseAutomatedGrokHealthTime(account.Extra[automatedGrokHealthLastFailureAtKey])
	if !ok || now.Before(failedAt) {
		return false
	}
	class := automatedGrokFailureClass(strings.TrimSpace(anyString(account.Extra[automatedGrokHealthFailureClassKey])))
	switch class {
	case automatedGrokFailureRateLimited:
		return now.Sub(failedAt) >= automatedGrokHealthRateLimitCooldown
	case automatedGrokFailureUpstream, automatedGrokFailureTransport:
		return now.Sub(failedAt) >= automatedGrokHealthTransientCooldown
	default:
		return false
	}
}

func automatedGrokHasFreshSuccess(account *Account, now time.Time) bool {
	observedAt, ok := parseAutomatedGrokHealthTime(account.Extra[automatedGrokHealthLastSuccessAtKey])
	if !ok || now.Before(observedAt) {
		return false
	}
	return now.Sub(observedAt) <= automatedGrokHealthSuccessTTL
}

func parseAutomatedGrokHealthTime(value any) (time.Time, bool) {
	parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(anyString(value)))
	return parsed, err == nil && !parsed.IsZero()
}

func classifyAutomatedGrokScheduledFailure(message string) automatedGrokFailureClass {
	if isPermanentGrokScheduledTestFailure(message) {
		return automatedGrokFailurePermissionDenied
	}
	statusCode, responseBody, ok := grokScheduledTestHTTPResponse(message)
	if ok {
		if isGrokFreeUsageExhaustedResponse(statusCode, responseBody) {
			return automatedGrokFailureFreeUsageExhausted
		}
		switch {
		case statusCode == 402:
			return automatedGrokFailureQuotaExhausted
		case statusCode == 429:
			return automatedGrokFailureRateLimited
		case statusCode == 401:
			return automatedGrokFailureCredential
		case statusCode == 403:
			return automatedGrokFailureForbidden
		case statusCode >= 500:
			return automatedGrokFailureUpstream
		}
	}

	lower := strings.ToLower(strings.TrimSpace(message))
	if strings.Contains(lower, "access token is expired") ||
		strings.Contains(lower, "access token is missing") ||
		strings.Contains(lower, "refresh token") {
		return automatedGrokFailureCredential
	}
	if strings.Contains(lower, "timeout") || strings.Contains(lower, "connection reset") ||
		strings.Contains(lower, "connection refused") || strings.Contains(lower, "no such host") ||
		strings.Contains(lower, "temporary network") {
		return automatedGrokFailureTransport
	}
	return automatedGrokFailureUnknown
}

func grokScheduledTestHTTPStatus(message string) (int, bool) {
	statusCode, _, ok := grokScheduledTestHTTPResponse(message)
	return statusCode, ok
}

func grokScheduledTestHTTPResponse(message string) (int, []byte, bool) {
	const prefix = "Grok Responses API returned "
	trimmed := strings.TrimSpace(message)
	if !strings.HasPrefix(trimmed, prefix) {
		return 0, nil, false
	}
	rest := strings.TrimPrefix(trimmed, prefix)
	separator := strings.IndexByte(rest, ':')
	if separator <= 0 {
		return 0, nil, false
	}
	statusCode, err := strconv.Atoi(strings.TrimSpace(rest[:separator]))
	if err != nil {
		return 0, nil, false
	}
	return statusCode, []byte(strings.TrimSpace(rest[separator+1:])), true
}
