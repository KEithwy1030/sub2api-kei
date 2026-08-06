//go:build unit

package handler

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
	"github.com/stretchr/testify/require"
)

func TestChatCompletionsGrokFresh429Failover(t *testing.T) {
	tests := []struct {
		name   string
		stream bool
	}{
		{name: "non-streaming", stream: false},
		{name: "streaming", stream: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, repo, upstream, router, cleanup := newGrokCredentialFailoverHandler(t, "first_429")
			defer cleanup()

			body := `{"model":"grok","messages":[{"role":"user","content":"hello"}],"stream":false}`
			if tt.stream {
				body = `{"model":"grok","messages":[{"role":"user","content":"hello"}],"stream":true}`
			}
			recorder := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/openai/v1/chat/completions", bytes.NewBufferString(body))
			req.Header.Set("Content-Type", "application/json")

			router.ServeHTTP(recorder, req)

			require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
			require.Equal(t, []int64{801, 802}, upstream.accountHits())
			require.Equal(t, []int64{801}, repo.rateLimitedAccountIDs())
			require.Empty(t, recorder.Header().Get("Retry-After"), "failed-account headers must not leak")

			requestURLs, authorization := upstream.requests()
			require.Equal(t, []string{
				xai.DefaultCLIBaseURL + "/responses",
				xai.DefaultCLIBaseURL + "/responses",
			}, requestURLs)
			require.Equal(t, []string{"Bearer expired", "Bearer healthy-access"}, authorization)
			require.NotContains(t, recorder.Body.String(), "rate limited")
			require.NotContains(t, recorder.Body.String(), "expired")
		})
	}
}
