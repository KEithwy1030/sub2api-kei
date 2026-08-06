package service

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestBuildGrokResponsesRequestUsesOfficialWebSearchHelperHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	request := httptest.NewRequest("POST", "/v1/responses", nil)
	request.Header.Set("X-Grok-Client-Mode", "headless")
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = request
	body := []byte(`{"model":"grok-4.5","input":"latest result","tools":[{"type":"web_search"}],"store":false,"temperature":0.1,"top_p":0.95,"max_output_tokens":8192}`)
	account := &Account{Type: AccountTypeOAuth, Platform: PlatformGrok}

	req, err := buildGrokResponsesRequest(context.Background(), c, account, body, "oauth-token", "", nil)
	require.NoError(t, err)
	require.Equal(t, "true", req.Header.Get(grokWebSearchHelperMarkerHeader))
	require.Equal(t, "xai-grok-cli", req.Header.Get("X-XAI-Token-Auth"))
	require.Equal(t, "authenticate-response", req.Header.Get("X-AuthenticateResponse"))
	require.Equal(t, "headless", req.Header.Get("X-Grok-Client-Mode"))
	require.Empty(t, req.Header.Get("X-Grok-Client-Version"))
	require.Empty(t, req.Header.Get("X-Grok-Client-Identifier"))
	require.Empty(t, req.Header.Get("X-Grok-Model-Override"))
	require.Empty(t, req.Header.Get("User-Agent"))
}

func TestGrokWebSearchHelperSignatureDoesNotMatchGenericResponses(t *testing.T) {
	require.True(t, isGrokCLIWebSearchHelperBody([]byte(`{"input":"q","tools":[{"type":"web_search"}],"store":false,"temperature":0.1,"top_p":0.95,"max_output_tokens":8192}`)))
	require.False(t, isGrokCLIWebSearchHelperBody([]byte(`{"input":"q","tools":[{"type":"web_search"}],"store":false}`)))
	require.False(t, isGrokCLIWebSearchHelperBody([]byte(`{"input":"q","tools":[{"type":"web_search"},{"type":"x_search"}],"store":false,"temperature":0.1,"top_p":0.95,"max_output_tokens":8192}`)))
}
