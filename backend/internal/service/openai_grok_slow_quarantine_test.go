//go:build unit

package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type grokSlowQuarantineModelLimitCall struct {
	accountID int64
	model     string
	until     time.Time
	reason    string
}

type grokSlowQuarantineAccountRepo struct {
	AccountRepository
	mu      sync.Mutex
	calls   chan grokSlowQuarantineModelLimitCall
	results chan error
}

func (r *grokSlowQuarantineAccountRepo) SetModelRateLimitIfInactive(_ context.Context, id int64, model string, until time.Time, reason string, _ time.Time) (bool, error) {
	call := grokSlowQuarantineModelLimitCall{accountID: id, model: model, until: until}
	call.reason = reason
	r.mu.Lock()
	r.mu.Unlock()
	r.calls <- call
	if r.results != nil {
		return true, <-r.results
	}
	return true, nil
}

func TestEvaluateGrokSlowTTFTQuarantineRequiresConsecutiveSlowSamples(t *testing.T) {
	repo := &grokSlowQuarantineAccountRepo{calls: make(chan grokSlowQuarantineModelLimitCall, 4)}
	cfg := &config.Config{}
	cfg.Gateway.OpenAIScheduler.GrokSlowQuarantineEnabled = true
	cfg.Gateway.OpenAIScheduler.GrokSlowQuarantineGroupIDs = []int64{8}
	cfg.Gateway.OpenAIScheduler.GrokSlowQuarantineTTFTMs = 20_000
	cfg.Gateway.OpenAIScheduler.GrokSlowQuarantineConsecutive = 2
	cfg.Gateway.OpenAIScheduler.GrokSlowQuarantineSeconds = 900
	svc := &OpenAIGatewayService{accountRepo: repo, cfg: cfg}
	account := &Account{ID: 544, Platform: PlatformGrok, Type: AccountTypeOAuth}
	groupID := int64(8)
	model := "grok-4.5"

	slow := 25_000
	fast := 2_000
	svc.EvaluateGrokSlowTTFTQuarantine(context.Background(), &groupID, account, model, &slow)
	svc.EvaluateGrokSlowTTFTQuarantine(context.Background(), &groupID, account, model, &fast)
	svc.EvaluateGrokSlowTTFTQuarantine(context.Background(), &groupID, account, model, &slow)
	select {
	case call := <-repo.calls:
		t.Fatalf("unexpected quarantine before consecutive threshold: %#v", call)
	default:
	}

	svc.EvaluateGrokSlowTTFTQuarantine(context.Background(), &groupID, account, model, &slow)
	require.True(t, svc.isGrokSlowTTFTQuarantined(account, model))

	select {
	case call := <-repo.calls:
		require.Equal(t, account.ID, call.accountID)
		require.Equal(t, model, call.model)
		require.Contains(t, call.reason, "grok slow ttft quarantine")
		require.True(t, call.until.After(time.Now().Add(14*time.Minute)))
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for durable quarantine")
	}

	// Additional slow samples inside the active window must not create duplicate writes.
	svc.EvaluateGrokSlowTTFTQuarantine(context.Background(), &groupID, account, model, &slow)
	select {
	case call := <-repo.calls:
		t.Fatalf("unexpected duplicate quarantine: %#v", call)
	default:
	}
}

func TestEvaluateGrokSlowTTFTQuarantineIsGroupAndPlatformScoped(t *testing.T) {
	repo := &grokSlowQuarantineAccountRepo{calls: make(chan grokSlowQuarantineModelLimitCall, 4)}
	cfg := &config.Config{}
	cfg.Gateway.OpenAIScheduler.GrokSlowQuarantineEnabled = true
	cfg.Gateway.OpenAIScheduler.GrokSlowQuarantineGroupIDs = []int64{8}
	cfg.Gateway.OpenAIScheduler.GrokSlowQuarantineTTFTMs = 20_000
	cfg.Gateway.OpenAIScheduler.GrokSlowQuarantineConsecutive = 1
	cfg.Gateway.OpenAIScheduler.GrokSlowQuarantineSeconds = 900
	svc := &OpenAIGatewayService{accountRepo: repo, cfg: cfg}
	slow := 25_000
	group11 := int64(11)
	group8 := int64(8)

	svc.EvaluateGrokSlowTTFTQuarantine(context.Background(), &group11, &Account{ID: 1, Platform: PlatformGrok, Type: AccountTypeOAuth}, "grok-4.5", &slow)
	svc.EvaluateGrokSlowTTFTQuarantine(context.Background(), &group8, &Account{ID: 2, Platform: PlatformOpenAI, Type: AccountTypeOAuth}, "grok-4.5", &slow)
	select {
	case call := <-repo.calls:
		t.Fatalf("unexpected out-of-scope quarantine: %#v", call)
	default:
	}
}

func TestEvaluateGrokSlowTTFTQuarantineMapsRequestedModelOnce(t *testing.T) {
	repo := &grokSlowQuarantineAccountRepo{calls: make(chan grokSlowQuarantineModelLimitCall, 1)}
	cfg := &config.Config{}
	cfg.Gateway.OpenAIScheduler.GrokSlowQuarantineEnabled = true
	cfg.Gateway.OpenAIScheduler.GrokSlowQuarantineGroupIDs = []int64{8}
	cfg.Gateway.OpenAIScheduler.GrokSlowQuarantineTTFTMs = 20_000
	cfg.Gateway.OpenAIScheduler.GrokSlowQuarantineConsecutive = 1
	cfg.Gateway.OpenAIScheduler.GrokSlowQuarantineSeconds = 900
	svc := &OpenAIGatewayService{accountRepo: repo, cfg: cfg}
	account := &Account{
		ID:       545,
		Platform: PlatformGrok,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"model_mapping": map[string]any{
				"claude-sonnet-5": "grok-4.5",
				"grok-4.5":        "unexpected-second-map",
			},
		},
	}
	groupID := int64(8)
	slow := 25_000

	svc.EvaluateGrokSlowTTFTQuarantine(context.Background(), &groupID, account, "claude-sonnet-5", &slow)

	select {
	case call := <-repo.calls:
		require.Equal(t, "grok-4.5", call.model)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for durable quarantine")
	}
}

func TestEvaluateGrokSlowTTFTQuarantineRetriesAfterPersistenceFailure(t *testing.T) {
	repo := &grokSlowQuarantineAccountRepo{
		calls:   make(chan grokSlowQuarantineModelLimitCall, 2),
		results: make(chan error, 2),
	}
	repo.results <- errors.New("database unavailable")
	repo.results <- nil
	cfg := &config.Config{}
	cfg.Gateway.OpenAIScheduler.GrokSlowQuarantineEnabled = true
	cfg.Gateway.OpenAIScheduler.GrokSlowQuarantineGroupIDs = []int64{8}
	cfg.Gateway.OpenAIScheduler.GrokSlowQuarantineTTFTMs = 20_000
	cfg.Gateway.OpenAIScheduler.GrokSlowQuarantineConsecutive = 2
	cfg.Gateway.OpenAIScheduler.GrokSlowQuarantineSeconds = 900
	svc := &OpenAIGatewayService{accountRepo: repo, cfg: cfg}
	account := &Account{ID: 546, Platform: PlatformGrok, Type: AccountTypeOAuth}
	groupID := int64(8)
	slow := 25_000

	svc.EvaluateGrokSlowTTFTQuarantine(context.Background(), &groupID, account, "grok-4.5", &slow)
	svc.EvaluateGrokSlowTTFTQuarantine(context.Background(), &groupID, account, "grok-4.5", &slow)
	select {
	case <-repo.calls:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for failed persistence attempt")
	}
	require.Eventually(t, func() bool {
		return !svc.isGrokSlowTTFTQuarantined(account, "grok-4.5")
	}, 2*time.Second, 10*time.Millisecond)

	svc.EvaluateGrokSlowTTFTQuarantine(context.Background(), &groupID, account, "grok-4.5", &slow)
	select {
	case call := <-repo.calls:
		require.Equal(t, account.ID, call.accountID)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for persistence retry")
	}
	require.Eventually(t, func() bool {
		return svc.isGrokSlowTTFTQuarantined(account, "grok-4.5")
	}, 2*time.Second, 10*time.Millisecond)
}

func TestPrepareGrokMessagesBodyForAffinityStripsOnlyCrossAccountHistory(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"claude-sonnet-5","messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"x","signature":"account-bound"}]}]}`)
	account := &Account{ID: 7, Platform: PlatformGrok, Type: AccountTypeOAuth}

	newContext := func() *gin.Context {
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		return c
	}

	crossAccount := newContext()
	SetGrokMessagesAccountSwitch(crossAccount, 6, account.ID)
	stripped, changed := prepareGrokMessagesBodyForAffinity(crossAccount, account, body)
	require.True(t, changed)
	require.False(t, gjson.GetBytes(stripped, "messages.0.content.0.signature").Exists())

	sticky := newContext()
	SetGrokMessagesAccountSwitch(sticky, account.ID, account.ID)
	preserved, changed := prepareGrokMessagesBodyForAffinity(sticky, account, body)
	require.False(t, changed)
	require.Equal(t, "account-bound", gjson.GetBytes(preserved, "messages.0.content.0.signature").String())

	unknown := newContext()
	preserved, changed = prepareGrokMessagesBodyForAffinity(unknown, account, body)
	require.False(t, changed)
	require.Equal(t, "account-bound", gjson.GetBytes(preserved, "messages.0.content.0.signature").String())
}

func TestScheduledRecoveryPreservesActiveGrokSlowTTFTQuarantine(t *testing.T) {
	resetAt := time.Now().Add(10 * time.Minute).UTC().Format(time.RFC3339)
	account := &Account{
		ID:       42,
		Status:   StatusActive,
		Platform: PlatformGrok,
		Type:     AccountTypeOAuth,
		Extra: map[string]any{
			modelRateLimitsKey: map[string]any{
				"grok-4.5": map[string]any{
					"rate_limit_reset_at": resetAt,
					"reason":              "grok slow ttft quarantine: 25000ms (2 consecutive)",
				},
			},
		},
	}
	repo := &grokSlowRecoveryAccountRepo{account: account}
	svc := &RateLimitService{accountRepo: repo}

	result, err := svc.RecoverAccountAfterSuccessfulTest(context.Background(), account.ID)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.ClearedRateLimit)
	require.True(t, repo.clearRateLimitCalled)
	require.True(t, repo.selectiveClearCalled)
}

func TestFastProbeRecoveryClearsDurableAndRuntimeGrokSlowTTFTQuarantine(t *testing.T) {
	resetAt := time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339)
	account := &Account{
		ID:       43,
		Status:   StatusActive,
		Platform: PlatformGrok,
		Type:     AccountTypeOAuth,
		Extra: map[string]any{
			modelRateLimitsKey: map[string]any{
				"grok-4.5": map[string]any{
					"rate_limit_reset_at": resetAt,
					"reason":              "grok slow ttft quarantine: 25000ms (2 consecutive)",
				},
			},
		},
	}
	repo := &grokSlowRecoveryAccountRepo{account: account}
	gateway := &OpenAIGatewayService{}
	key := openAIAccountModelKey{AccountID: account.ID, Model: "grok-4.5"}
	gateway.grokSlowTTFTQuarantineUntil.Store(key, time.Now().Add(24*time.Hour))
	gateway.grokSlowTTFTQuarantineStats.Store(key, &grokSlowTTFTQuarantineStat{until: time.Now().Add(24 * time.Hour)})
	svc := &RateLimitService{accountRepo: repo, runtimeBlocker: gateway}

	cleared, err := svc.RecoverGrokSlowTTFTAfterFastProbe(context.Background(), account.ID)

	require.NoError(t, err)
	require.True(t, cleared)
	require.True(t, repo.probeClearCalled)
	_, runtimeBlocked := gateway.grokSlowTTFTQuarantineUntil.Load(key)
	require.False(t, runtimeBlocked)
	_, statsRemain := gateway.grokSlowTTFTQuarantineStats.Load(key)
	require.False(t, statsRemain)
}

type grokSlowRecoveryAccountRepo struct {
	AccountRepository
	account              *Account
	clearRateLimitCalled bool
	selectiveClearCalled bool
	probeClearCalled     bool
}

func (r *grokSlowRecoveryAccountRepo) GetByID(_ context.Context, _ int64) (*Account, error) {
	return r.account, nil
}

func (r *grokSlowRecoveryAccountRepo) ClearRateLimit(_ context.Context, _ int64) error {
	r.clearRateLimitCalled = true
	return nil
}

func (r *grokSlowRecoveryAccountRepo) ClearAntigravityQuotaScopes(_ context.Context, _ int64) error {
	return nil
}

func (r *grokSlowRecoveryAccountRepo) ClearTempUnschedulable(_ context.Context, _ int64) error {
	return nil
}

func (r *grokSlowRecoveryAccountRepo) ClearModelRateLimitsExceptActiveReasonPrefix(_ context.Context, _ int64, prefix string, _ time.Time) error {
	r.selectiveClearCalled = true
	if prefix != grokSlowTTFTQuarantineReasonPrefix {
		return fmt.Errorf("unexpected reason prefix: %s", prefix)
	}
	return nil
}

func (r *grokSlowRecoveryAccountRepo) ClearModelRateLimitsByReasonPrefix(_ context.Context, _ int64, prefix string) (bool, error) {
	r.probeClearCalled = true
	if prefix != grokSlowTTFTQuarantineReasonPrefix {
		return false, fmt.Errorf("unexpected reason prefix: %s", prefix)
	}
	return true, nil
}
