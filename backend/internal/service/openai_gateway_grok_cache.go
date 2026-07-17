package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	grokFreePromptCacheTokenLimit = int64(2_000_000)
	grokFreePromptCacheToolsJSON  = `[{"type":"web_search"},{"type":"x_search"}]`
	grokPromptCacheHeader         = "X-Grok-Conv-Id"
	grokPromptCacheModelAlias     = "grok-4.5-cached"

	grokPromptCacheStateExtraKey     = "grok_prompt_cache_state"
	grokPromptCacheCheckedAtExtraKey = "grok_prompt_cache_checked_at"
	grokPromptCacheSuccessesExtraKey = "grok_prompt_cache_successes"
	grokPromptCacheFailuresExtraKey  = "grok_prompt_cache_failures"

	grokPromptCacheStateUnknown     = "unknown"
	grokPromptCacheStateSupported   = "supported"
	grokPromptCacheStateUnsupported = "unsupported"

	grokPromptCacheUnsupportedAfter = 3
	grokPromptCacheSupportedRecheck = 7 * 24 * time.Hour
	grokPromptCacheUnsupportedRetry = 24 * time.Hour
)

type grokPromptCacheProbeOutcome struct {
	HTTPStatus      int
	CacheReadTokens int
}

// applyGrokFreeResponsesPromptCacheRoute keeps Grok Free OAuth requests with
// Responses-style function tools on xAI's cache-capable mixed-tools route.
// The cache key is isolated by downstream API key and model before forwarding.
func applyGrokFreeResponsesPromptCacheRoute(body, intentSource []byte, account *Account, apiKeyID int64, upstreamModel string) ([]byte, error) {
	if apiKeyID <= 0 || !isKnownGrokFreeForPromptCache(account) {
		return body, nil
	}
	rawCacheKey := strings.TrimSpace(gjson.GetBytes(body, "prompt_cache_key").String())
	if rawCacheKey == "" {
		return body, nil
	}

	identity := generateSessionUUID(fmt.Sprintf(
		"grok-prompt-cache:v1:%d:%s:%s",
		apiKeyID,
		strings.ToLower(strings.TrimSpace(upstreamModel)),
		rawCacheKey,
	))
	out, err := sjson.SetBytes(body, "prompt_cache_key", identity)
	if err != nil {
		return nil, err
	}

	intentTools := gjson.GetBytes(intentSource, "tools")
	intentToolChoice := gjson.GetBytes(intentSource, "tool_choice")
	if !intentTools.Exists() && !intentToolChoice.Exists() {
		out, err = sjson.SetRawBytes(out, "tools", []byte(grokFreePromptCacheToolsJSON))
		if err != nil {
			return nil, err
		}
		return sjson.SetBytes(out, "tool_choice", "none")
	}
	if !isGrokFreeResponsesFunctionToolIntent(intentTools, intentToolChoice) {
		return out, nil
	}
	return appendMissingGrokFreePromptCacheTools(out)
}

func applyGrokFreePromptCacheHeader(headers http.Header, body []byte, account *Account) {
	if headers == nil || !isKnownGrokFreeForPromptCache(account) {
		return
	}
	if identity := strings.TrimSpace(gjson.GetBytes(body, "prompt_cache_key").String()); identity != "" {
		headers.Set(grokPromptCacheHeader, identity)
	}
}

func isKnownGrokFreeForPromptCache(account *Account) bool {
	if account == nil || !account.IsGrokOAuth() {
		return false
	}

	freeSignal := false
	paidSignal := false
	classifyTier := func(tier string) {
		switch strings.ToLower(strings.TrimSpace(tier)) {
		case "free", "grok-free", "grok_free", "free-tier", "free_tier", "basic", "grok-basic", "grok_basic":
			freeSignal = true
		case "", "unknown", "n/a", "none":
		default:
			paidSignal = true
		}
	}

	classifyTier(account.GetCredential("subscription_tier"))
	if snapshot, err := grokQuotaSnapshotFromExtra(account.Extra); err == nil && snapshot != nil {
		classifyTier(snapshot.SubscriptionTier)
		if snapshot.Tokens != nil && snapshot.Tokens.Limit != nil && *snapshot.Tokens.Limit == grokFreePromptCacheTokenLimit {
			freeSignal = true
		}
	}
	return freeSignal && !paidSignal
}

func isGrokPromptCacheModelAlias(model string) bool {
	return strings.EqualFold(strings.TrimSpace(model), grokPromptCacheModelAlias)
}

func grokPromptCacheState(account *Account) string {
	if account == nil || account.Extra == nil {
		return grokPromptCacheStateUnknown
	}
	state, _ := account.Extra[grokPromptCacheStateExtraKey].(string)
	switch strings.ToLower(strings.TrimSpace(state)) {
	case grokPromptCacheStateSupported:
		return grokPromptCacheStateSupported
	case grokPromptCacheStateUnsupported:
		return grokPromptCacheStateUnsupported
	default:
		return grokPromptCacheStateUnknown
	}
}

func grokPromptCacheExtraInt(account *Account, key string) int {
	if account == nil || account.Extra == nil {
		return 0
	}
	switch value := account.Extra[key].(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	case json.Number:
		parsed, _ := strconv.Atoi(value.String())
		return parsed
	default:
		return 0
	}
}

func grokPromptCacheCheckedAt(account *Account) time.Time {
	if account == nil || account.Extra == nil {
		return time.Time{}
	}
	raw, _ := account.Extra[grokPromptCacheCheckedAtExtraKey].(string)
	checkedAt, _ := time.Parse(time.RFC3339, strings.TrimSpace(raw))
	return checkedAt
}

func isGrokPromptCacheVerified(account *Account) bool {
	return isKnownGrokFreeForPromptCache(account) && grokPromptCacheState(account) == grokPromptCacheStateSupported
}

func shouldProbeGrokPromptCache(account *Account, now time.Time) bool {
	if !isKnownGrokFreeForPromptCache(account) {
		return false
	}
	checkedAt := grokPromptCacheCheckedAt(account)
	if checkedAt.IsZero() {
		return true
	}
	switch grokPromptCacheState(account) {
	case grokPromptCacheStateSupported:
		return now.Sub(checkedAt) >= grokPromptCacheSupportedRecheck
	case grokPromptCacheStateUnsupported:
		return now.Sub(checkedAt) >= grokPromptCacheUnsupportedRetry
	default:
		return true
	}
}

func grokPromptCacheProbeUpdates(account *Account, outcome grokPromptCacheProbeOutcome, now time.Time) map[string]any {
	// Permission, quota and upstream failures are not evidence about cache capability.
	if outcome.HTTPStatus != http.StatusOK {
		return nil
	}

	successes := grokPromptCacheExtraInt(account, grokPromptCacheSuccessesExtraKey)
	failures := grokPromptCacheExtraInt(account, grokPromptCacheFailuresExtraKey)
	state := grokPromptCacheState(account)
	if outcome.CacheReadTokens > 0 {
		successes++
		failures = 0
		state = grokPromptCacheStateSupported
	} else {
		failures++
		if failures >= grokPromptCacheUnsupportedAfter {
			state = grokPromptCacheStateUnsupported
		}
	}
	return map[string]any{
		grokPromptCacheStateExtraKey:     state,
		grokPromptCacheCheckedAtExtraKey: now.UTC().Format(time.RFC3339),
		grokPromptCacheSuccessesExtraKey: successes,
		grokPromptCacheFailuresExtraKey:  failures,
	}
}

func resolveGrokUpstreamModel(account *Account, requestedModel string) string {
	if account == nil {
		return requestedModel
	}
	if mapped, matched := account.ResolveMappedModel(requestedModel); matched {
		return mapped
	}
	if isGrokPromptCacheModelAlias(requestedModel) && isGrokPromptCacheVerified(account) {
		return "grok-4.5"
	}
	return requestedModel
}

func (s *OpenAIGatewayService) observeGrokPromptCacheHit(account *Account, usage *OpenAIUsage) {
	if s == nil || s.accountRepo == nil || account == nil || usage == nil || usage.CacheReadInputTokens <= 0 || !isKnownGrokFreeForPromptCache(account) {
		return
	}
	if grokPromptCacheState(account) == grokPromptCacheStateSupported && time.Since(grokPromptCacheCheckedAt(account)) < 6*time.Hour {
		return
	}
	updates := grokPromptCacheProbeUpdates(account, grokPromptCacheProbeOutcome{
		HTTPStatus:      http.StatusOK,
		CacheReadTokens: usage.CacheReadInputTokens,
	}, time.Now())
	if len(updates) == 0 {
		return
	}
	go func(accountID int64, values map[string]any) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.accountRepo.UpdateExtra(ctx, accountID, values)
	}(account.ID, updates)
}

func isGrokFreeResponsesFunctionToolIntent(tools, toolChoice gjson.Result) bool {
	if !tools.IsArray() || len(tools.Array()) == 0 {
		return false
	}
	for _, tool := range tools.Array() {
		if !tool.IsObject() || strings.TrimSpace(tool.Get("type").String()) != "function" {
			return false
		}
		if strings.TrimSpace(tool.Get("name").String()) == "" || tool.Get("function").Exists() {
			return false
		}
	}
	if !toolChoice.Exists() {
		return true
	}
	return toolChoice.Type == gjson.String && strings.TrimSpace(toolChoice.String()) == "auto"
}

func appendMissingGrokFreePromptCacheTools(body []byte) ([]byte, error) {
	tools := gjson.GetBytes(body, "tools")
	if !tools.IsArray() || len(tools.Array()) == 0 {
		return body, nil
	}

	merged := make([]json.RawMessage, 0, len(tools.Array())+2)
	present := make(map[string]bool, 2)
	hasFunction := false
	for _, tool := range tools.Array() {
		toolType := strings.TrimSpace(tool.Get("type").String())
		switch toolType {
		case "function":
			name := strings.TrimSpace(tool.Get("name").String())
			if !tool.IsObject() || name == "" || tool.Get("function").Exists() {
				return body, nil
			}
			if name == "web_search" || name == "x_search" {
				if !present[name] {
					raw, err := json.Marshal(map[string]string{"type": name})
					if err != nil {
						return nil, err
					}
					merged = append(merged, raw)
					present[name] = true
				}
				continue
			}
			hasFunction = true
			merged = append(merged, json.RawMessage(tool.Raw))
		case "web_search", "x_search":
			if !present[toolType] {
				merged = append(merged, json.RawMessage(tool.Raw))
				present[toolType] = true
			}
		default:
			return body, nil
		}
	}
	if !hasFunction {
		return body, nil
	}
	for _, toolType := range []string{"web_search", "x_search"} {
		if present[toolType] {
			continue
		}
		raw, err := json.Marshal(map[string]string{"type": toolType})
		if err != nil {
			return nil, err
		}
		merged = append(merged, raw)
	}
	encoded, err := json.Marshal(merged)
	if err != nil {
		return nil, err
	}
	return sjson.SetRawBytes(body, "tools", encoded)
}
