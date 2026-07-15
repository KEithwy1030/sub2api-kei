package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/handler/dto"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

const (
	grokSSOImportConcurrency = 3
	grokSSOImportAttempts    = 3
)

type GrokOAuthHandler struct {
	grokOAuthService *service.GrokOAuthService
	adminService     service.AdminService
	accountRepo      service.AccountRepository
	quotaService     *service.GrokQuotaService
	httpUpstream     service.HTTPUpstream
}

func NewGrokOAuthHandler(
	grokOAuthService *service.GrokOAuthService,
	adminService service.AdminService,
	accountRepo service.AccountRepository,
	quotaService *service.GrokQuotaService,
	httpUpstream service.HTTPUpstream,
) *GrokOAuthHandler {
	return &GrokOAuthHandler{
		grokOAuthService: grokOAuthService,
		adminService:     adminService,
		accountRepo:      accountRepo,
		quotaService:     quotaService,
		httpUpstream:     httpUpstream,
	}
}

type GrokGenerateAuthURLRequest struct {
	ProxyID     *int64 `json:"proxy_id"`
	RedirectURI string `json:"redirect_uri"`
}

func (h *GrokOAuthHandler) GenerateAuthURL(c *gin.Context) {
	var req GrokGenerateAuthURLRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		req = GrokGenerateAuthURLRequest{}
	}
	result, err := h.grokOAuthService.GenerateAuthURL(c.Request.Context(), req.ProxyID, req.RedirectURI)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}

type GrokExchangeCodeRequest struct {
	SessionID   string `json:"session_id" binding:"required"`
	Code        string `json:"code" binding:"required"`
	State       string `json:"state"`
	RedirectURI string `json:"redirect_uri"`
	ProxyID     *int64 `json:"proxy_id"`
}

func (h *GrokOAuthHandler) ExchangeCode(c *gin.Context) {
	var req GrokExchangeCodeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	tokenInfo, err := h.grokOAuthService.ExchangeCode(c.Request.Context(), &service.GrokExchangeCodeInput{
		SessionID:   req.SessionID,
		Code:        req.Code,
		State:       req.State,
		RedirectURI: req.RedirectURI,
		ProxyID:     req.ProxyID,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, tokenInfo)
}

type GrokRefreshTokenRequest struct {
	RefreshToken string `json:"refresh_token"`
	RT           string `json:"rt"`
	ClientID     string `json:"client_id"`
	ProxyID      *int64 `json:"proxy_id"`
	ProbeModel   string `json:"probe_model"`
}

type GrokChatPreflightResult struct {
	Usable            bool   `json:"usable"`
	Model             string `json:"model"`
	StatusCode        int    `json:"status_code,omitempty"`
	Reason            string `json:"reason"`
	RetryAfterSeconds int64  `json:"retry_after_seconds,omitempty"`
}

type GrokRefreshTokenResult struct {
	TokenInfo *service.GrokTokenInfo  `json:"token_info"`
	Preflight GrokChatPreflightResult `json:"preflight"`
}

const (
	grokPreflightModel   = "grok-4.5"
	grokPreflightTimeout = 30 * time.Second
)

func (h *GrokOAuthHandler) RefreshToken(c *gin.Context) {
	var req GrokRefreshTokenRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	refreshToken := strings.TrimSpace(req.RefreshToken)
	if refreshToken == "" {
		refreshToken = strings.TrimSpace(req.RT)
	}
	if refreshToken == "" {
		response.BadRequest(c, "refresh_token is required")
		return
	}

	var proxyURL string
	if req.ProxyID != nil {
		proxy, err := h.adminService.GetProxy(c.Request.Context(), *req.ProxyID)
		if err == nil && proxy != nil {
			proxyURL = proxy.URL()
		}
	}
	tokenInfo, err := h.grokOAuthService.RefreshToken(c.Request.Context(), refreshToken, proxyURL, req.ClientID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	preflight := h.probeGrokChat(c.Request.Context(), tokenInfo.AccessToken, proxyURL, req.ProbeModel)
	response.Success(c, &GrokRefreshTokenResult{TokenInfo: tokenInfo, Preflight: preflight})
}

func (h *GrokOAuthHandler) probeGrokChat(ctx context.Context, accessToken, proxyURL, model string) GrokChatPreflightResult {
	model = strings.TrimSpace(model)
	if model == "" {
		model = grokPreflightModel
	}
	result := GrokChatPreflightResult{Model: model, Reason: "GROK_PREFLIGHT_NOT_CONFIGURED"}
	if h == nil || h.httpUpstream == nil {
		return result
	}
	body, err := json.Marshal(map[string]any{
		"model": model, "input": ".", "max_output_tokens": 1, "store": false, "stream": false,
	})
	if err != nil {
		result.Reason = "GROK_PREFLIGHT_REQUEST_BUILD_FAILED"
		return result
	}
	targetURL, err := xai.BuildResponsesURL(xai.DefaultCLIBaseURL)
	if err != nil {
		result.Reason = "GROK_PREFLIGHT_REQUEST_BUILD_FAILED"
		return result
	}
	callCtx, cancel := context.WithTimeout(ctx, grokPreflightTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(callCtx, http.MethodPost, targetURL, bytes.NewReader(body))
	if err != nil {
		result.Reason = "GROK_PREFLIGHT_REQUEST_BUILD_FAILED"
		return result
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := h.httpUpstream.Do(req, proxyURL, 0, 1)
	if err != nil {
		result.Reason = "GROK_PREFLIGHT_REQUEST_FAILED"
		return result
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	result.StatusCode = resp.StatusCode
	if resp.StatusCode == http.StatusTooManyRequests {
		result.RetryAfterSeconds = int64((2 * time.Minute).Seconds())
		if snapshot := xai.ParseQuotaHeaders(resp.Header, resp.StatusCode); snapshot != nil && snapshot.RetryAfterSeconds != nil && *snapshot.RetryAfterSeconds > 0 {
			result.RetryAfterSeconds = int64(*snapshot.RetryAfterSeconds)
		}
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		result.Usable = true
		result.Reason = "GROK_PREFLIGHT_OK"
		return result
	}
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		result.Reason = "GROK_PREFLIGHT_ACCESS_TOKEN_REJECTED"
	case http.StatusPaymentRequired:
		result.Reason = "GROK_PREFLIGHT_SPENDING_LIMIT"
	case http.StatusForbidden:
		result.Reason = "GROK_PREFLIGHT_CHAT_PERMISSION_DENIED"
	case http.StatusUpgradeRequired:
		result.Reason = "GROK_PREFLIGHT_CLI_IDENTITY_REJECTED"
	case http.StatusTooManyRequests:
		result.Reason = "GROK_PREFLIGHT_RATE_LIMITED"
	default:
		result.Reason = "GROK_PREFLIGHT_UPSTREAM_REJECTED"
	}
	return result
}

func (h *GrokOAuthHandler) RefreshAccountToken(c *gin.Context) {
	accountID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid account ID")
		return
	}
	account, err := h.adminService.GetAccount(c.Request.Context(), accountID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if account.Platform != service.PlatformGrok {
		response.BadRequest(c, "Account platform does not match Grok OAuth endpoint")
		return
	}
	if !account.IsOAuth() {
		response.BadRequest(c, "Cannot refresh non-OAuth account credentials")
		return
	}
	tokenInfo, err := h.grokOAuthService.RefreshAccountToken(c.Request.Context(), account)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	newCredentials := h.grokOAuthService.BuildAccountCredentials(tokenInfo)
	newCredentials = service.MergeCredentials(account.Credentials, newCredentials)
	if baseURL := strings.TrimSpace(account.GetCredential("base_url")); baseURL != "" {
		newCredentials["base_url"] = baseURL
	}
	updatedAccount, err := h.adminService.UpdateAccount(c.Request.Context(), accountID, &service.UpdateAccountInput{
		Credentials: newCredentials,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, dto.AccountFromService(updatedAccount))
}

func (h *GrokOAuthHandler) CreateAccountFromOAuth(c *gin.Context) {
	var req struct {
		SessionID   string  `json:"session_id" binding:"required"`
		Code        string  `json:"code" binding:"required"`
		State       string  `json:"state"`
		RedirectURI string  `json:"redirect_uri"`
		ProxyID     *int64  `json:"proxy_id"`
		Name        string  `json:"name"`
		Concurrency int     `json:"concurrency"`
		Priority    int     `json:"priority"`
		GroupIDs    []int64 `json:"group_ids"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	tokenInfo, err := h.grokOAuthService.ExchangeCode(c.Request.Context(), &service.GrokExchangeCodeInput{
		SessionID:   req.SessionID,
		Code:        req.Code,
		State:       req.State,
		RedirectURI: req.RedirectURI,
		ProxyID:     req.ProxyID,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	credentials := h.grokOAuthService.BuildAccountCredentials(tokenInfo)

	name := strings.TrimSpace(req.Name)
	if name == "" && tokenInfo.Email != "" {
		name = tokenInfo.Email
	}
	if name == "" {
		name = "Grok OAuth Account"
	}

	account, err := h.adminService.CreateAccount(c.Request.Context(), &service.CreateAccountInput{
		Name:        name,
		Platform:    service.PlatformGrok,
		Type:        service.AccountTypeOAuth,
		Credentials: credentials,
		ProxyID:     req.ProxyID,
		Concurrency: req.Concurrency,
		Priority:    req.Priority,
		GroupIDs:    req.GroupIDs,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, dto.AccountFromService(account))
}

type GrokSSOToOAuthRequest struct {
	SSOTokens          []string       `json:"sso_tokens"`
	SSOToken           string         `json:"sso_token"`
	Name               string         `json:"name"`
	Notes              *string        `json:"notes"`
	ProxyID            *int64         `json:"proxy_id"`
	GroupIDs           []int64        `json:"group_ids"`
	Credentials        map[string]any `json:"credentials"`
	Extra              map[string]any `json:"extra"`
	Concurrency        int            `json:"concurrency"`
	LoadFactor         *int           `json:"load_factor"`
	Priority           int            `json:"priority"`
	RateMultiplier     *float64       `json:"rate_multiplier"`
	ExpiresAt          *int64         `json:"expires_at"`
	AutoPauseOnExpired *bool          `json:"auto_pause_on_expired"`
}

type GrokSSOToOAuthItemResult struct {
	Index     int                      `json:"index"`
	Name      string                   `json:"name,omitempty"`
	Email     string                   `json:"email,omitempty"`
	Account   *dto.Account             `json:"account,omitempty"`
	Preflight *GrokChatPreflightResult `json:"preflight,omitempty"`
	Error     string                   `json:"error,omitempty"`
}

type GrokSSOToOAuthResponse struct {
	Created []GrokSSOToOAuthItemResult `json:"created"`
	Failed  []GrokSSOToOAuthItemResult `json:"failed"`
}

type grokSSOImportJob struct {
	index int
	token string
}

type grokSSOImportWorkerResult struct {
	created bool
	item    GrokSSOToOAuthItemResult
}

func (h *GrokOAuthHandler) CreateAccountsFromSSO(c *gin.Context) {
	var req GrokSSOToOAuthRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	tokens := normalizeSSOImportTokens(req.SSOTokens, req.SSOToken)
	if len(tokens) == 0 {
		response.BadRequest(c, "sso_tokens is required")
		return
	}

	ctx := c.Request.Context()
	workerCount := grokSSOImportConcurrency
	if len(tokens) < workerCount {
		workerCount = len(tokens)
	}
	jobs := make(chan grokSSOImportJob)
	items := make([]grokSSOImportWorkerResult, len(tokens))
	var wg sync.WaitGroup
	for i := 0; i < workerCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range jobs {
				items[job.index] = h.safeCreateAccountFromSSOToken(ctx, req, job.token, job.index+1, len(tokens))
			}
		}()
	}
	for i, token := range tokens {
		jobs <- grokSSOImportJob{index: i, token: token}
	}
	close(jobs)
	wg.Wait()

	result := GrokSSOToOAuthResponse{
		Created: make([]GrokSSOToOAuthItemResult, 0, len(tokens)),
		Failed:  make([]GrokSSOToOAuthItemResult, 0),
	}
	for _, item := range items {
		if item.created {
			result.Created = append(result.Created, item.item)
		} else {
			result.Failed = append(result.Failed, item.item)
		}
	}
	response.Success(c, result)
}

func (h *GrokOAuthHandler) safeCreateAccountFromSSOToken(ctx context.Context, req GrokSSOToOAuthRequest, token string, index, total int) (result grokSSOImportWorkerResult) {
	defer func() {
		if recovered := recover(); recovered != nil {
			slog.Error("grok_sso_import_worker_panic", "index", index, "recover", recovered)
			result = grokSSOImportWorkerResult{
				item: GrokSSOToOAuthItemResult{
					Index: index,
					Error: fmt.Sprintf("internal worker panic: %v", recovered),
				},
			}
		}
	}()
	return h.createAccountFromSSOToken(ctx, req, token, index, total)
}

func (h *GrokOAuthHandler) createAccountFromSSOToken(ctx context.Context, req GrokSSOToOAuthRequest, token string, index, total int) grokSSOImportWorkerResult {
	tokenInfo, err := h.convertGrokSSOWithRetry(ctx, token, req.ProxyID, index)
	if err != nil {
		return grokSSOImportWorkerResult{item: GrokSSOToOAuthItemResult{Index: index, Error: grokSSOImportErrorMessage(err)}}
	}
	proxyURL := ""
	if req.ProxyID != nil {
		if proxy, proxyErr := h.adminService.GetProxy(ctx, *req.ProxyID); proxyErr == nil && proxy != nil {
			proxyURL = proxy.URL()
		}
	}
	preflight := h.probeGrokChat(ctx, tokenInfo.AccessToken, proxyURL, grokPreflightModel)

	credentials := h.grokOAuthService.BuildAccountCredentials(tokenInfo)
	credentials = service.MergeCredentials(cloneGrokSSOMap(req.Credentials), credentials)
	credentials["base_url"] = xai.DefaultCLIBaseURL
	credentials["model_mapping"] = map[string]any{grokPreflightModel: grokPreflightModel}
	name := grokSSOImportAccountName(req.Name, tokenInfo, index, total)
	expiresAt, autoPauseOnExpired := grokSSOImportExpiry(req.ExpiresAt, req.AutoPauseOnExpired, tokenInfo)
	account, err := h.adminService.CreateAccount(ctx, &service.CreateAccountInput{
		Name:               name,
		Notes:              req.Notes,
		Platform:           service.PlatformGrok,
		Type:               service.AccountTypeOAuth,
		Credentials:        credentials,
		Extra:              cloneGrokSSOMap(req.Extra),
		ProxyID:            req.ProxyID,
		Concurrency:        req.Concurrency,
		LoadFactor:         req.LoadFactor,
		Priority:           req.Priority,
		RateMultiplier:     req.RateMultiplier,
		GroupIDs:           append([]int64(nil), req.GroupIDs...),
		ExpiresAt:          expiresAt,
		AutoPauseOnExpired: autoPauseOnExpired,
	})
	if err != nil {
		return grokSSOImportWorkerResult{item: GrokSSOToOAuthItemResult{Index: index, Name: name, Email: tokenInfo.Email, Error: grokSSOImportErrorMessage(err)}}
	}
	if err := h.applyGrokImportPreflightState(ctx, account, preflight); err != nil {
		_ = h.adminService.SetAccountError(ctx, account.ID, "failed to persist Grok preflight state: "+err.Error())
		return grokSSOImportWorkerResult{created: true, item: GrokSSOToOAuthItemResult{
			Index: index, Name: name, Email: tokenInfo.Email, Account: dto.AccountFromService(account), Preflight: &preflight,
			Error: "GROK_PREFLIGHT_STATE_PERSIST_FAILED",
		}}
	}
	return grokSSOImportWorkerResult{
		created: true,
		item: GrokSSOToOAuthItemResult{
			Index: index, Name: name, Email: tokenInfo.Email, Account: dto.AccountFromService(account), Preflight: &preflight,
		},
	}
}

func (h *GrokOAuthHandler) convertGrokSSOWithRetry(ctx context.Context, token string, proxyID *int64, index int) (*service.GrokTokenInfo, error) {
	var lastErr error
	for attempt := 0; attempt < grokSSOImportAttempts; attempt++ {
		tokenInfo, err := h.grokOAuthService.ConvertFromSSO(ctx, token, proxyID)
		if err == nil {
			return tokenInfo, nil
		}
		lastErr = err
		if !grokSSOImportRetryable(err) || attempt == grokSSOImportAttempts-1 {
			break
		}
		backoff := time.Duration(2<<attempt)*time.Second + time.Duration(index%3)*500*time.Millisecond
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	return nil, lastErr
}

func grokSSOImportRetryable(err error) bool {
	status := infraerrors.FromError(err)
	if status == nil {
		return false
	}
	switch status.Reason {
	case "GROK_SSO_UPSTREAM_FAILED", "GROK_SSO_CONVERSION_FAILED", "GROK_SSO_TIMEOUT":
		return true
	default:
		return false
	}
}

func (h *GrokOAuthHandler) applyGrokImportPreflightState(ctx context.Context, account *service.Account, preflight GrokChatPreflightResult) error {
	if account == nil || preflight.Usable {
		return nil
	}
	if h.accountRepo == nil {
		return fmt.Errorf("account repository is unavailable")
	}

	cooldown := time.Duration(0)
	switch preflight.StatusCode {
	case 0:
		cooldown = 2 * time.Minute
	case http.StatusUnauthorized:
		cooldown = 10 * time.Minute
	case http.StatusForbidden:
		cooldown = 30 * time.Minute
	case http.StatusTooManyRequests:
		cooldown = time.Duration(preflight.RetryAfterSeconds) * time.Second
		if cooldown <= 0 {
			cooldown = 2 * time.Minute
		}
	default:
		if preflight.StatusCode >= 500 {
			cooldown = 2 * time.Minute
		}
	}
	if cooldown > 0 {
		return h.accountRepo.SetTempUnschedulable(ctx, account.ID, time.Now().Add(cooldown), preflight.Reason)
	}
	return h.accountRepo.SetError(ctx, account.ID, preflight.Reason)
}

func grokSSOImportExpiry(requestExpiresAt *int64, requestAutoPause *bool, tokenInfo *service.GrokTokenInfo) (*int64, *bool) {
	if tokenInfo == nil || strings.TrimSpace(tokenInfo.RefreshToken) != "" || tokenInfo.ExpiresAt <= 0 {
		return requestExpiresAt, requestAutoPause
	}

	expiresAt := tokenInfo.ExpiresAt
	if requestExpiresAt != nil && *requestExpiresAt > 0 && *requestExpiresAt < expiresAt {
		expiresAt = *requestExpiresAt
	}
	autoPause := true
	return &expiresAt, &autoPause
}

func cloneGrokSSOMap(source map[string]any) map[string]any {
	if source == nil {
		return nil
	}
	clone := make(map[string]any, len(source))
	for key, value := range source {
		clone[key] = cloneGrokSSOValue(value)
	}
	return clone
}

func cloneGrokSSOValue(value any) any {
	switch v := value.(type) {
	case map[string]any:
		return cloneGrokSSOMap(v)
	case []any:
		clone := make([]any, len(v))
		for i, item := range v {
			clone[i] = cloneGrokSSOValue(item)
		}
		return clone
	default:
		return value
	}
}

func normalizeSSOImportTokens(tokens []string, single string) []string {
	items := make([]string, 0, len(tokens)+1)
	if strings.TrimSpace(single) != "" {
		items = append(items, single)
	}
	items = append(items, tokens...)
	seen := make(map[string]struct{}, len(items))
	result := make([]string, 0, len(items))
	for _, item := range items {
		parts := strings.Split(strings.NewReplacer(",", "\n", "\r", "\n").Replace(item), "\n")
		for _, token := range parts {
			if token = xai.NormalizeSSOToken(token); token == "" {
				continue
			}
			if _, ok := seen[token]; ok {
				continue
			}
			seen[token] = struct{}{}
			result = append(result, token)
		}
	}
	return result
}

func grokSSOImportAccountName(base string, tokenInfo *service.GrokTokenInfo, index, total int) string {
	base = strings.TrimSpace(base)
	if base == "" && tokenInfo != nil {
		base = strings.TrimSpace(tokenInfo.Email)
	}
	if base == "" {
		base = "Grok OAuth Account"
	}
	if total > 1 {
		return base + " #" + strconv.Itoa(index)
	}
	return base
}

func grokSSOImportErrorMessage(err error) string {
	status := infraerrors.FromError(err)
	if status == nil {
		return ""
	}
	if status.Reason != "" {
		return status.Reason + ": " + status.Message
	}
	return status.Message
}

func (h *GrokOAuthHandler) QueryQuota(c *gin.Context) {
	accountID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid account ID")
		return
	}
	if h.quotaService == nil {
		response.BadRequest(c, "grok quota service is not enabled")
		return
	}
	result, err := h.quotaService.ProbeUsage(c.Request.Context(), accountID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}

func (h *GrokOAuthHandler) ResetQuota(c *gin.Context) {
	accountID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid account ID")
		return
	}
	if h.quotaService == nil {
		response.BadRequest(c, "grok quota service is not enabled")
		return
	}
	result, err := h.quotaService.ResetQuota(c.Request.Context(), accountID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}

func (h *GrokOAuthHandler) RuntimeSanity(c *gin.Context) {
	response.Success(c, xai.RuntimeSanity())
}
