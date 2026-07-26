package service

import (
	"context"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/robfig/cron/v3"
)

const scheduledTestDefaultMaxWorkers = 10

// ScheduledTestRunnerService periodically scans due test plans and executes them.
type ScheduledTestRunnerService struct {
	planRepo       ScheduledTestPlanRepository
	scheduledSvc   *ScheduledTestService
	accountTestSvc *AccountTestService
	accountRepo    scheduledTestCleanupAccountRepository
	rateLimitSvc   *RateLimitService
	cfg            *config.Config

	cron      *cron.Cron
	startOnce sync.Once
	stopOnce  sync.Once
}

// NewScheduledTestRunnerService creates a new runner.
func NewScheduledTestRunnerService(
	planRepo ScheduledTestPlanRepository,
	scheduledSvc *ScheduledTestService,
	accountTestSvc *AccountTestService,
	accountRepo AccountRepository,
	rateLimitSvc *RateLimitService,
	cfg *config.Config,
) *ScheduledTestRunnerService {
	return &ScheduledTestRunnerService{
		planRepo:       planRepo,
		scheduledSvc:   scheduledSvc,
		accountTestSvc: accountTestSvc,
		accountRepo:    accountRepo,
		rateLimitSvc:   rateLimitSvc,
		cfg:            cfg,
	}
}

// Start begins the cron ticker (every minute).
func (s *ScheduledTestRunnerService) Start() {
	if s == nil {
		return
	}
	s.startOnce.Do(func() {
		loc := time.Local
		if s.cfg != nil {
			if parsed, err := time.LoadLocation(s.cfg.Timezone); err == nil && parsed != nil {
				loc = parsed
			}
		}

		c := cron.New(cron.WithParser(scheduledTestCronParser), cron.WithLocation(loc))
		_, err := c.AddFunc("* * * * *", func() { s.runScheduled() })
		if err != nil {
			logger.LegacyPrintf("service.scheduled_test_runner", "[ScheduledTestRunner] not started (invalid schedule): %v", err)
			return
		}
		s.cron = c
		s.cron.Start()
		logger.LegacyPrintf("service.scheduled_test_runner", "[ScheduledTestRunner] started (tick=every minute)")
	})
}

// Stop gracefully shuts down the cron scheduler.
func (s *ScheduledTestRunnerService) Stop() {
	if s == nil {
		return
	}
	s.stopOnce.Do(func() {
		if s.cron != nil {
			ctx := s.cron.Stop()
			select {
			case <-ctx.Done():
			case <-time.After(3 * time.Second):
				logger.LegacyPrintf("service.scheduled_test_runner", "[ScheduledTestRunner] cron stop timed out")
			}
		}
	})
}

func (s *ScheduledTestRunnerService) runScheduled() {
	// Delay 10s so execution lands at ~:10 of each minute instead of :00.
	time.Sleep(10 * time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	now := time.Now()
	plans, err := s.planRepo.ListDue(ctx, now)
	if err != nil {
		logger.LegacyPrintf("service.scheduled_test_runner", "[ScheduledTestRunner] ListDue error: %v", err)
		return
	}
	if len(plans) == 0 {
		return
	}

	logger.LegacyPrintf("service.scheduled_test_runner", "[ScheduledTestRunner] found %d due plans", len(plans))

	sem := make(chan struct{}, scheduledTestDefaultMaxWorkers)
	var wg sync.WaitGroup

	for _, plan := range plans {
		sem <- struct{}{}
		wg.Add(1)
		go func(p *ScheduledTestPlan) {
			defer wg.Done()
			defer func() { <-sem }()
			s.runOnePlan(ctx, p)
		}(plan)
	}

	wg.Wait()
}

func (s *ScheduledTestRunnerService) runOnePlan(ctx context.Context, plan *ScheduledTestPlan) {
	result, err := s.accountTestSvc.RunTestBackground(ctx, plan.AccountID, plan.ModelID)
	if err != nil {
		logger.LegacyPrintf("service.scheduled_test_runner", "[ScheduledTestRunner] plan=%d RunTestBackground error: %v", plan.ID, err)
		return
	}

	resultSaved := true
	if err := s.scheduledSvc.SaveResult(ctx, plan.ID, plan.MaxResults, result); err != nil {
		resultSaved = false
		logger.LegacyPrintf("service.scheduled_test_runner", "[ScheduledTestRunner] plan=%d SaveResult error: %v", plan.ID, err)
	}

	healthApplied := false
	if resultSaved {
		healthApplied = s.recordAutomatedGrokHealth(ctx, plan, result)
	}
	// Cold-pool accounts need stable recovery evidence before they re-enter the
	// production scheduler. Other accounts retain the existing one-shot recovery.
	if result.Status == "success" && plan.AutoRecover {
		var account *Account
		var accountErr error
		if s.accountRepo != nil {
			account, accountErr = s.accountRepo.GetByID(ctx, plan.AccountID)
		}
		if accountErr == nil && s.shouldRecoverGrokSlowTTFTFromProbe(account, result) {
			recovered, recoverErr := s.rateLimitSvc.RecoverGrokSlowTTFTAfterFastProbe(ctx, plan.AccountID)
			if recoverErr != nil {
				logger.LegacyPrintf("service.scheduled_test_runner", "[ScheduledTestRunner] plan=%d Grok slow-TTFT probe recovery failed: account=%d err=%v", plan.ID, plan.AccountID, recoverErr)
			} else if recovered {
				logger.LegacyPrintf("service.scheduled_test_runner", "[ScheduledTestRunner] plan=%d cleared Grok slow-TTFT quarantine after fast probe: account=%d latency_ms=%d", plan.ID, plan.AccountID, result.LatencyMs)
			}
		}
		if accountErr == nil && isAutomatedGrokQuarantined(account) {
			s.tryPromoteQuarantinedAutomatedGrok(ctx, plan, result)
		} else {
			s.tryRecoverAccount(ctx, plan.AccountID, plan.ID)
		}
	}

	// Retirement re-reads the account after recovery, so a successful probe can
	// clear stale invalid_grant state before an isolation decision.
	if resultSaved && healthApplied {
		s.tryRetirePermanentlyUnavailableAutomatedGrok(ctx, plan, result)
	}

	nextRun, err := computeNextRun(plan.CronExpression, time.Now())
	if err != nil {
		logger.LegacyPrintf("service.scheduled_test_runner", "[ScheduledTestRunner] plan=%d computeNextRun error: %v", plan.ID, err)
		return
	}

	if err := s.planRepo.UpdateAfterRun(ctx, plan.ID, time.Now(), nextRun); err != nil {
		logger.LegacyPrintf("service.scheduled_test_runner", "[ScheduledTestRunner] plan=%d UpdateAfterRun error: %v", plan.ID, err)
	}
}

func (s *ScheduledTestRunnerService) shouldRecoverGrokSlowTTFTFromProbe(account *Account, result *ScheduledTestResult) bool {
	if s == nil || s.cfg == nil || s.rateLimitSvc == nil || account == nil || result == nil || result.Status != "success" {
		return false
	}
	thresholdMs := s.cfg.Gateway.OpenAIScheduler.GrokSlowQuarantineTTFTMs
	return thresholdMs > 0 && result.LatencyMs <= int64(thresholdMs) &&
		isActiveGrokSlowTTFTModelRateLimit(account, time.Now())
}

func (s *ScheduledTestRunnerService) recordAutomatedGrokHealth(ctx context.Context, plan *ScheduledTestPlan, result *ScheduledTestResult) bool {
	if s == nil || s.accountRepo == nil || plan == nil || result == nil {
		return false
	}
	account, err := s.accountRepo.GetByID(ctx, plan.AccountID)
	if err != nil || !automatedGrokHealthManaged(account) {
		return false
	}

	observedAt := result.FinishedAt
	if observedAt.IsZero() {
		observedAt = time.Now()
	}
	var updates map[string]any
	if result.Status == "success" {
		updates = automatedGrokHealthSuccessUpdates(observedAt)
	} else {
		updates = automatedGrokHealthFailureUpdates(classifyAutomatedGrokScheduledFailure(result.ErrorMessage), observedAt)
	}
	applied, err := persistAutomatedGrokHealthUpdates(ctx, s.accountRepo, account, observedAt, updates)
	if err != nil {
		logger.LegacyPrintf("service.scheduled_test_runner", "[ScheduledTestRunner] plan=%d Grok health snapshot update failed: %v", plan.ID, err)
		return false
	}
	return applied
}

// tryRecoverAccount attempts to recover an account from recoverable runtime state.
func (s *ScheduledTestRunnerService) tryRecoverAccount(ctx context.Context, accountID int64, planID int64) {
	if s.rateLimitSvc == nil {
		return
	}

	recovery, err := s.rateLimitSvc.RecoverAccountAfterSuccessfulTest(ctx, accountID)
	if err != nil {
		logger.LegacyPrintf("service.scheduled_test_runner", "[ScheduledTestRunner] plan=%d auto-recover failed: %v", planID, err)
		return
	}
	if recovery == nil {
		return
	}

	if recovery.ClearedError {
		logger.LegacyPrintf("service.scheduled_test_runner", "[ScheduledTestRunner] plan=%d auto-recover: account=%d recovered from error status", planID, accountID)
	}
	if recovery.ClearedRateLimit {
		logger.LegacyPrintf("service.scheduled_test_runner", "[ScheduledTestRunner] plan=%d auto-recover: account=%d cleared rate-limit/runtime state", planID, accountID)
	}
}
