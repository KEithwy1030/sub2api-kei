package service

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

const (
	automatedGrokCleanupPolicyKey       = "automation_cleanup_policy"
	automatedGrokCleanupPolicyValue     = "grok-free-permanent-v1"
	automatedGrokCleanupEnabledAtKey    = "automation_cleanup_enabled_at"
	automatedGrokCleanupSourceKey       = "automation_source"
	automatedGrokCleanupSourceValue     = "mac-grok-register"
	automatedGrokCleanupMinAge          = 24 * time.Hour
	automatedGrokCleanupMinFailureSpan  = 24 * time.Hour
	automatedGrokCleanupRequiredResults = 3
	automatedGrokCleanupResultWindow    = 20
	automatedGrokQuarantineStateKey     = "automation_quarantine_state"
	automatedGrokQuarantineReasonKey    = "automation_quarantine_reason"
	automatedGrokQuarantinedAtKey       = "automation_quarantined_at"
	automatedGrokQuarantineStateCold    = "cold"
	automatedGrokPromotionResults       = 2
	automatedGrokPromotionMinSpan       = 12 * time.Hour
)

type scheduledTestCleanupAccountRepository interface {
	GetByID(ctx context.Context, id int64) (*Account, error)
	Delete(ctx context.Context, id int64) error
	UpdateExtra(ctx context.Context, id int64, updates map[string]any) error
}

type automatedGrokQuarantineRepository interface {
	QuarantineAutomatedGrokIfUnchanged(
		ctx context.Context,
		id int64,
		expectedUpdatedAt time.Time,
		expectedCredentials map[string]any,
		expectedHealthObservedAt time.Time,
		expectedFailureClass string,
		reason string,
		coldCron string,
	) (bool, error)
}

type automatedGrokPromotionRepository interface {
	PromoteAutomatedGrokIfUnchanged(
		ctx context.Context,
		id int64,
		expectedUpdatedAt time.Time,
		expectedCredentials map[string]any,
		expectedHealthObservedAt time.Time,
		activeCron string,
	) (bool, error)
}

func (s *ScheduledTestRunnerService) tryRetirePermanentlyUnavailableAutomatedGrok(
	ctx context.Context,
	plan *ScheduledTestPlan,
	current *ScheduledTestResult,
) bool {
	if s == nil || s.accountRepo == nil || s.scheduledSvc == nil || plan == nil || current == nil {
		return false
	}

	account, err := s.accountRepo.GetByID(ctx, plan.AccountID)
	if err != nil || !automatedGrokCleanupEnabled(account, time.Now()) {
		return false
	}
	permanentCredentialState := isPermanentAutomatedGrokCredentialState(account)
	if !shouldRetireAutomatedGrok(account, current) {
		return false
	}
	if !permanentCredentialState {
		results, err := s.scheduledSvc.ListResults(ctx, plan.ID, automatedGrokCleanupResultWindow)
		if err != nil {
			logger.LegacyPrintf("service.scheduled_test_runner", "[ScheduledTestRunner] plan=%d Grok cleanup result lookup failed: %v", plan.ID, err)
			return false
		}
		if !hasStablePermanentGrokFailures(results) {
			return false
		}
	}

	// Re-read immediately before deletion. A successful test, admin edit, or
	// OAuth reauthorization changes UpdatedAt/credentials and wins this race.
	latest, err := s.accountRepo.GetByID(ctx, account.ID)
	if err != nil || !automatedGrokCleanupEnabled(latest, time.Now()) ||
		!latest.UpdatedAt.Equal(account.UpdatedAt) ||
		!reflect.DeepEqual(latest.Credentials, account.Credentials) ||
		(permanentCredentialState && !isPermanentAutomatedGrokCredentialState(latest)) {
		return false
	}
	if err := s.accountRepo.Delete(ctx, latest.ID); err != nil {
		logger.LegacyPrintf("service.scheduled_test_runner", "[ScheduledTestRunner] plan=%d automated Grok retirement failed: account=%d err=%v", plan.ID, latest.ID, err)
		return false
	}
	logger.LegacyPrintf("service.scheduled_test_runner", "[ScheduledTestRunner] plan=%d retired permanently unavailable automated Grok account=%d", plan.ID, latest.ID)
	return true
}

func (s *ScheduledTestRunnerService) tryPromoteQuarantinedAutomatedGrok(
	ctx context.Context,
	plan *ScheduledTestPlan,
	current *ScheduledTestResult,
) bool {
	if s == nil || s.accountRepo == nil || s.scheduledSvc == nil || plan == nil || current == nil || current.Status != "success" {
		return false
	}
	account, err := s.accountRepo.GetByID(ctx, plan.AccountID)
	if err != nil || !isAutomatedGrokQuarantined(account) {
		return false
	}
	results, err := s.scheduledSvc.ListResults(ctx, plan.ID, automatedGrokPromotionResults)
	if err != nil || !hasStableGrokRecovery(results) {
		return false
	}
	latest, err := s.accountRepo.GetByID(ctx, account.ID)
	if err != nil || !isAutomatedGrokQuarantined(latest) ||
		!reflect.DeepEqual(latest.Credentials, account.Credentials) {
		return false
	}
	repo, ok := s.accountRepo.(automatedGrokPromotionRepository)
	if !ok {
		return false
	}
	promoted, err := repo.PromoteAutomatedGrokIfUnchanged(
		ctx,
		latest.ID,
		latest.UpdatedAt,
		latest.Credentials,
		current.FinishedAt,
		automatedGrokActiveProbeCron(latest.ID),
	)
	if err != nil {
		logger.LegacyPrintf("service.scheduled_test_runner", "[ScheduledTestRunner] plan=%d automated Grok promotion failed: account=%d err=%v", plan.ID, latest.ID, err)
		return false
	}
	if !promoted {
		return false
	}
	plan.CronExpression = automatedGrokActiveProbeCron(latest.ID)
	logger.LegacyPrintf("service.scheduled_test_runner", "[ScheduledTestRunner] plan=%d promoted automated Grok account=%d after stable recovery", plan.ID, latest.ID)
	return true
}

func isAutomatedGrokQuarantined(account *Account) bool {
	return automatedGrokHealthManaged(account) && account.Status == StatusError && !account.Schedulable &&
		strings.TrimSpace(anyString(account.Extra[automatedGrokQuarantineStateKey])) == automatedGrokQuarantineStateCold
}

func hasStableGrokRecovery(results []*ScheduledTestResult) bool {
	if len(results) < automatedGrokPromotionResults {
		return false
	}
	newest := results[0]
	oldest := results[automatedGrokPromotionResults-1]
	if newest == nil || oldest == nil || newest.Status != "success" || oldest.Status != "success" {
		return false
	}
	return !newest.FinishedAt.IsZero() && !oldest.StartedAt.IsZero() &&
		newest.FinishedAt.Sub(oldest.StartedAt) >= automatedGrokPromotionMinSpan
}

func automatedGrokColdPoolCron(accountID int64) string {
	return fmt.Sprintf("%d %d * * *", positiveGrokCronMod(accountID*37, 60), positiveGrokCronMod(accountID*17, 24))
}

func automatedGrokActiveProbeCron(accountID int64) string {
	minute := positiveGrokCronMod(accountID*37, 60)
	startHour := positiveGrokCronMod(accountID*17, 6)
	return fmt.Sprintf("%d %d,%d,%d,%d * * *", minute, startHour, startHour+6, startHour+12, startHour+18)
}

func positiveGrokCronMod(value int64, modulus int64) int64 {
	result := value % modulus
	if result < 0 {
		result += modulus
	}
	return result
}

func shouldRetireAutomatedGrok(account *Account, current *ScheduledTestResult) bool {
	if account == nil || current == nil {
		return false
	}
	if isPermanentAutomatedGrokCredentialState(account) {
		return true
	}
	return current.Status == "failed" && isPermanentGrokScheduledTestFailure(current.ErrorMessage)
}

func automatedGrokCleanupEnabled(account *Account, now time.Time) bool {
	if account == nil || !account.IsGrokOAuth() || account.Extra == nil {
		return false
	}
	if strings.TrimSpace(anyString(account.Extra[automatedGrokCleanupSourceKey])) != automatedGrokCleanupSourceValue ||
		strings.TrimSpace(anyString(account.Extra[automatedGrokCleanupPolicyKey])) != automatedGrokCleanupPolicyValue {
		return false
	}
	enabledAt, err := time.Parse(time.RFC3339, strings.TrimSpace(anyString(account.Extra[automatedGrokCleanupEnabledAtKey])))
	if err != nil || account.CreatedAt.IsZero() {
		return false
	}
	eligibleAt := account.CreatedAt.Add(automatedGrokCleanupMinAge)
	if enabledAt.After(eligibleAt) {
		eligibleAt = enabledAt
	}
	return !now.Before(eligibleAt)
}

func hasStablePermanentGrokFailures(results []*ScheduledTestResult) bool {
	var newest time.Time
	var oldest time.Time
	failureCount := 0
	for _, result := range results {
		if result == nil || result.Status != "failed" || !isPermanentGrokScheduledTestFailure(result.ErrorMessage) {
			break
		}
		failureCount++
		if newest.IsZero() || result.FinishedAt.After(newest) {
			newest = result.FinishedAt
		}
		if oldest.IsZero() || result.StartedAt.Before(oldest) {
			oldest = result.StartedAt
		}
	}
	return failureCount >= automatedGrokCleanupRequiredResults &&
		!newest.IsZero() && !oldest.IsZero() &&
		newest.Sub(oldest) >= automatedGrokCleanupMinFailureSpan
}

func isPermanentAutomatedGrokCredentialState(account *Account) bool {
	if account == nil || account.Status != StatusError {
		return false
	}
	message := strings.ToLower(strings.TrimSpace(account.ErrorMessage))
	switch {
	case strings.HasPrefix(message, "grok oauth credential reconciliation: missing refresh token"):
		return true
	case strings.HasPrefix(message, "authentication failed (401): refresh_token missing"):
		return true
	case strings.HasPrefix(message, "oauth 401 (no refresh_token):"):
		return true
	case strings.HasPrefix(message, "token refresh failed (non-retryable):"):
		return strings.Contains(message, "invalid_grant") ||
			strings.Contains(message, "invalid_refresh_token") ||
			strings.Contains(message, "token_expired") ||
			strings.Contains(message, "refresh_token_reused") ||
			strings.Contains(message, "refresh_token_invalidated") ||
			strings.Contains(message, "app_session_terminated") ||
			strings.Contains(message, "no refresh token available")
	default:
		return false
	}
}

func isPermanentGrokScheduledTestFailure(message string) bool {
	statusCode, responseBody, ok := grokScheduledTestHTTPResponse(message)
	if !ok {
		return false
	}
	return isPermanentGrokPermissionDeniedResponse(statusCode, responseBody)
}

func normalizeScheduledGrokErrorCode(value string) string {
	return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(value)), "-", "_")
}

func anyString(value any) string {
	text, _ := value.(string)
	return text
}
