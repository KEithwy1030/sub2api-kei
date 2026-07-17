package handler

import (
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// provider429FailoverCount keeps Grok's small 429 retry budget independent
// from earlier 401/403/5xx failovers in the same request.
func provider429FailoverCount(account *service.Account, statusCode, overallSwitchCount int, grok429SwitchCount *int) int {
	if account == nil || !account.IsGrokOAuth() || statusCode != http.StatusTooManyRequests || grok429SwitchCount == nil {
		return overallSwitchCount
	}
	(*grok429SwitchCount)++
	return *grok429SwitchCount
}
