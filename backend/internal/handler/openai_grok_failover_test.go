package handler

import (
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestProvider429FailoverCountSeparatesPriorGrokFailures(t *testing.T) {
	account := &service.Account{Platform: service.PlatformGrok, Type: service.AccountTypeOAuth}
	grok429SwitchCount := 0

	require.Equal(t, 8, provider429FailoverCount(account, http.StatusForbidden, 8, &grok429SwitchCount))
	require.Equal(t, 1, provider429FailoverCount(account, http.StatusTooManyRequests, 9, &grok429SwitchCount))
	require.Equal(t, 2, provider429FailoverCount(account, http.StatusTooManyRequests, 10, &grok429SwitchCount))
}
