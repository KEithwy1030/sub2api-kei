package service

import (
	"context"
	"encoding/json"
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
	automatedGrokCleanupMinFailureSpan  = 11 * time.Hour
	automatedGrokCleanupRequiredResults = 3
)

type scheduledTestCleanupAccountRepository interface {
	GetByID(ctx context.Context, id int64) (*Account, error)
	Delete(ctx context.Context, id int64) error
}

func (s *ScheduledTestRunnerService) tryRetirePermanentlyUnavailableAutomatedGrok(
	ctx context.Context,
	plan *ScheduledTestPlan,
	current *ScheduledTestResult,
) bool {
	if s == nil || s.accountRepo == nil || s.scheduledSvc == nil || plan == nil || current == nil || current.Status != "failed" {
		return false
	}

	account, err := s.accountRepo.GetByID(ctx, plan.AccountID)
	if err != nil || !automatedGrokCleanupEnabled(account, time.Now()) {
		return false
	}
	results, err := s.scheduledSvc.ListResults(ctx, plan.ID, automatedGrokCleanupRequiredResults)
	if err != nil {
		logger.LegacyPrintf("service.scheduled_test_runner", "[ScheduledTestRunner] plan=%d Grok cleanup result lookup failed: %v", plan.ID, err)
		return false
	}
	if !hasStablePermanentGrokFailures(results) {
		return false
	}

	// Re-read immediately before deletion. A successful test, admin edit, or OAuth
	// reauthorization changes UpdatedAt/credentials and wins over this cleanup pass.
	latest, err := s.accountRepo.GetByID(ctx, account.ID)
	if err != nil || !automatedGrokCleanupEnabled(latest, time.Now()) ||
		!latest.UpdatedAt.Equal(account.UpdatedAt) ||
		!reflect.DeepEqual(latest.Credentials, account.Credentials) {
		return false
	}
	if err := s.accountRepo.Delete(ctx, latest.ID); err != nil {
		logger.LegacyPrintf("service.scheduled_test_runner", "[ScheduledTestRunner] plan=%d automated Grok retirement failed: account=%d err=%v", plan.ID, latest.ID, err)
		return false
	}
	logger.LegacyPrintf("service.scheduled_test_runner", "[ScheduledTestRunner] plan=%d retired permanently unavailable automated Grok account=%d after %d stable failures", plan.ID, latest.ID, automatedGrokCleanupRequiredResults)
	return true
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
	return err == nil && !now.Before(enabledAt.Add(automatedGrokCleanupMinAge))
}

func hasStablePermanentGrokFailures(results []*ScheduledTestResult) bool {
	if len(results) < automatedGrokCleanupRequiredResults {
		return false
	}
	results = results[:automatedGrokCleanupRequiredResults]
	for _, result := range results {
		if result == nil || result.Status != "failed" || !isPermanentGrokScheduledTestFailure(result.ErrorMessage) {
			return false
		}
	}
	newest := results[0].FinishedAt
	oldest := results[len(results)-1].StartedAt
	return !newest.IsZero() && !oldest.IsZero() && newest.Sub(oldest) >= automatedGrokCleanupMinFailureSpan
}

func isPermanentGrokScheduledTestFailure(message string) bool {
	const prefix = "Grok Responses API returned 403:"
	trimmed := strings.TrimSpace(message)
	if !strings.HasPrefix(trimmed, prefix) {
		return false
	}
	body := strings.TrimSpace(strings.TrimPrefix(trimmed, prefix))
	var payload struct {
		Code  string `json:"code"`
		Error string `json:"error"`
	}
	if json.Unmarshal([]byte(body), &payload) != nil || normalizeScheduledGrokErrorCode(payload.Code) != "permission_denied" {
		return false
	}
	const deniedPrefix = "access to the chat endpoint is denied. please ensure you're using the correct credentials. if you believe this is a mistake, please"
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(payload.Error)), deniedPrefix)
}

func normalizeScheduledGrokErrorCode(value string) string {
	return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(value)), "-", "_")
}

func anyString(value any) string {
	text, _ := value.(string)
	return text
}
