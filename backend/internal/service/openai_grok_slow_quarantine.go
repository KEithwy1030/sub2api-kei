package service

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

const grokSlowTTFTQuarantineReasonPrefix = "grok slow ttft quarantine:"

type grokSlowTTFTQuarantineStat struct {
	mu          sync.Mutex
	consecutive int
	inFlight    bool
	until       time.Time
}

type grokSlowQuarantineWriter interface {
	SetModelRateLimitIfInactive(context.Context, int64, string, time.Time, string, time.Time) (bool, error)
}

type grokSlowQuarantineRecoveryRepository interface {
	ClearModelRateLimitsExceptActiveReasonPrefix(context.Context, int64, string, time.Time) error
}

type grokSlowQuarantineProbeRecoveryRepository interface {
	ClearModelRateLimitsByReasonPrefix(context.Context, int64, string) (bool, error)
}

type grokSlowQuarantineRuntimeClearer interface {
	ClearGrokSlowTTFTQuarantine(int64)
}

func (s *OpenAIGatewayService) EvaluateGrokSlowTTFTQuarantine(
	ctx context.Context,
	groupID *int64,
	account *Account,
	requestedModel string,
	firstTokenMs *int,
) {
	if s == nil || s.cfg == nil || account == nil || !account.IsGrokOAuth() || account.IsShadow() ||
		firstTokenMs == nil || *firstTokenMs <= 0 {
		return
	}

	cfg := s.cfg.Gateway.OpenAIScheduler
	if !cfg.GrokSlowQuarantineEnabled || groupID == nil || !containsInt64(cfg.GrokSlowQuarantineGroupIDs, *groupID) {
		return
	}

	model := openAIAccountModelTransientModel(canonicalOpenAIAccountSchedulingModel(account, requestedModel))
	if model == "" {
		return
	}
	key := openAIAccountModelKey{AccountID: account.ID, Model: model}
	value, _ := s.grokSlowTTFTQuarantineStats.LoadOrStore(key, &grokSlowTTFTQuarantineStat{})
	stat, _ := value.(*grokSlowTTFTQuarantineStat)
	if stat == nil {
		return
	}

	now := time.Now()
	stat.mu.Lock()
	if stat.inFlight || now.Before(stat.until) {
		stat.mu.Unlock()
		return
	}
	if *firstTokenMs <= cfg.GrokSlowQuarantineTTFTMs {
		stat.consecutive = 0
		stat.mu.Unlock()
		return
	}
	stat.consecutive++
	if stat.consecutive < cfg.GrokSlowQuarantineConsecutive {
		stat.mu.Unlock()
		return
	}

	until := now.Add(time.Duration(cfg.GrokSlowQuarantineSeconds) * time.Second)
	observedConsecutive := stat.consecutive
	stat.consecutive = 0
	stat.inFlight = true
	stat.until = until
	stat.mu.Unlock()

	// The runtime gate takes effect before the durable scheduler snapshot catches up.
	s.grokSlowTTFTQuarantineUntil.Store(key, until)
	reason := fmt.Sprintf("%s %dms (%d consecutive)", grokSlowTTFTQuarantineReasonPrefix, *firstTokenMs, observedConsecutive)
	s.persistGrokSlowTTFTQuarantine(ctx, key, until, reason, observedConsecutive, stat)
}

func (s *OpenAIGatewayService) persistGrokSlowTTFTQuarantine(
	ctx context.Context,
	key openAIAccountModelKey,
	until time.Time,
	reason string,
	observedConsecutive int,
	stat *grokSlowTTFTQuarantineStat,
) {
	persisted := false
	retryable := true
	defer func() {
		stat.mu.Lock()
		if !persisted {
			stat.until = time.Time{}
			if retryable {
				if retryFrom := observedConsecutive - 1; stat.consecutive < retryFrom {
					stat.consecutive = retryFrom
				}
			} else {
				stat.consecutive = 0
			}
			s.grokSlowTTFTQuarantineUntil.CompareAndDelete(key, until)
		}
		stat.inFlight = false
		stat.mu.Unlock()
	}()

	stateRepo, ok := s.accountRepo.(grokSlowQuarantineWriter)
	if !ok {
		slog.Warn("grok_slow_ttft_quarantine_persist_skipped", "account_id", key.AccountID, "model", key.Model, "reason", "conditional account repository unavailable")
		return
	}
	stateCtx, cancel := openAIAccountStateContext(ctx)
	defer cancel()
	applied, err := stateRepo.SetModelRateLimitIfInactive(stateCtx, key.AccountID, key.Model, until, reason, time.Now())
	if err != nil {
		slog.Warn("grok_slow_ttft_quarantine_persist_failed", "account_id", key.AccountID, "model", key.Model, "until", until.UTC(), "error", err)
		return
	}
	if !applied {
		retryable = false
		slog.Info("grok_slow_ttft_quarantine_skipped_active_model_limit", "account_id", key.AccountID, "model", key.Model)
		return
	}
	persisted = true
	slog.Info("grok_slow_ttft_quarantine_applied", "account_id", key.AccountID, "model", key.Model, "until", until.UTC(), "reason", reason)
}

func (s *OpenAIGatewayService) isGrokSlowTTFTQuarantined(account *Account, requestedModel string) bool {
	if s == nil || account == nil {
		return false
	}
	model := openAIAccountModelTransientModel(canonicalOpenAIAccountSchedulingModel(account, requestedModel))
	if model == "" {
		return false
	}
	key := openAIAccountModelKey{AccountID: account.ID, Model: model}
	rawUntil, ok := s.grokSlowTTFTQuarantineUntil.Load(key)
	if !ok {
		return false
	}
	until, ok := rawUntil.(time.Time)
	if !ok || !time.Now().Before(until) {
		s.grokSlowTTFTQuarantineUntil.Delete(key)
		return false
	}
	return true
}

func (s *OpenAIGatewayService) ClearGrokSlowTTFTQuarantine(accountID int64) {
	if s == nil || accountID <= 0 {
		return
	}
	s.grokSlowTTFTQuarantineUntil.Range(func(rawKey, _ any) bool {
		key, ok := rawKey.(openAIAccountModelKey)
		if ok && key.AccountID == accountID {
			s.grokSlowTTFTQuarantineUntil.Delete(rawKey)
		}
		return true
	})
	s.grokSlowTTFTQuarantineStats.Range(func(rawKey, _ any) bool {
		key, ok := rawKey.(openAIAccountModelKey)
		if ok && key.AccountID == accountID {
			s.grokSlowTTFTQuarantineStats.Delete(rawKey)
		}
		return true
	})
}

func (s *RateLimitService) RecoverGrokSlowTTFTAfterFastProbe(ctx context.Context, accountID int64) (bool, error) {
	if s == nil || s.accountRepo == nil || accountID <= 0 {
		return false, nil
	}
	account, err := s.accountRepo.GetByID(ctx, accountID)
	if err != nil {
		return false, err
	}
	if !isActiveGrokSlowTTFTModelRateLimit(account, time.Now()) {
		return false, nil
	}
	repo, ok := s.accountRepo.(grokSlowQuarantineProbeRecoveryRepository)
	if !ok {
		return false, fmt.Errorf("conditional Grok slow-quarantine probe recovery repository is unavailable")
	}
	cleared, err := repo.ClearModelRateLimitsByReasonPrefix(ctx, accountID, grokSlowTTFTQuarantineReasonPrefix)
	if err != nil || !cleared {
		return cleared, err
	}
	if runtime, ok := s.runtimeBlocker.(grokSlowQuarantineRuntimeClearer); ok {
		runtime.ClearGrokSlowTTFTQuarantine(accountID)
	}
	s.notifyAccountSchedulingBlockCleared(accountID)
	return true, nil
}

func isActiveGrokSlowTTFTModelRateLimit(account *Account, now time.Time) bool {
	if account == nil || account.Extra == nil {
		return false
	}
	rawLimits, ok := account.Extra[modelRateLimitsKey].(map[string]any)
	if !ok {
		return false
	}
	for _, rawLimit := range rawLimits {
		limit, ok := rawLimit.(map[string]any)
		if !ok || !strings.HasPrefix(strings.TrimSpace(anyString(limit["reason"])), grokSlowTTFTQuarantineReasonPrefix) {
			continue
		}
		resetAt, err := time.Parse(time.RFC3339, strings.TrimSpace(anyString(limit["rate_limit_reset_at"])))
		if err == nil && now.Before(resetAt) {
			return true
		}
	}
	return false
}
