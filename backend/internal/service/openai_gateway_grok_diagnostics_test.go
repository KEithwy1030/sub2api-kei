package service

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestGrokSessionDiagnosticFingerprint(t *testing.T) {
	fingerprint := GrokSessionDiagnosticFingerprint("session-hash-1")
	require.Len(t, fingerprint, 16)
	require.Equal(t, fingerprint, GrokSessionDiagnosticFingerprint(" session-hash-1 "))
	require.NotEqual(t, fingerprint, GrokSessionDiagnosticFingerprint("session-hash-2"))
	require.NotContains(t, fingerprint, "session")
	require.Empty(t, GrokSessionDiagnosticFingerprint(""))
}

func TestWithGrokPromptCacheDiagnostic(t *testing.T) {
	ctx := withGrokPromptCacheDiagnostic(context.Background(), []byte(`{"prompt_cache_key":"cache-secret"}`))
	diagnostic := grokPromptCacheDiagnosticFromContext(ctx)
	require.True(t, diagnostic.present)
	require.Len(t, diagnostic.fingerprint, 16)
	require.NotContains(t, diagnostic.fingerprint, "cache-secret")

	withoutKey := grokPromptCacheDiagnosticFromContext(withGrokPromptCacheDiagnostic(context.Background(), []byte(`{}`)))
	require.False(t, withoutKey.present)
	require.Empty(t, withoutKey.fingerprint)
}

func TestOpenAISessionDiagnosticSource(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil)

	require.Equal(t, "content_fallback", OpenAISessionDiagnosticSource(c, []byte(`{"model":"grok-4.5","input":"hello"}`)))
	require.Equal(t, "prompt_cache_key", OpenAISessionDiagnosticSource(c, []byte(`{"prompt_cache_key":"cache-key"}`)))
	c.Request.Header.Set("conversation_id", "conversation-id")
	require.Equal(t, "conversation_id_header", OpenAISessionDiagnosticSource(c, []byte(`{"prompt_cache_key":"cache-key"}`)))
	c.Request.Header.Set("session_id", "session-id")
	require.Equal(t, "session_id_header", OpenAISessionDiagnosticSource(c, nil))
}

func TestIsGrokReasoningStreamEvent(t *testing.T) {
	require.True(t, isGrokReasoningStreamEvent("response.reasoning_summary_text.delta", nil))
	require.True(t, isGrokReasoningStreamEvent("response.reasoning_text.delta", nil))
	require.True(t, isGrokReasoningStreamEvent("response.output_item.added", []byte(`{"item":{"type":"reasoning"}}`)))
	require.False(t, isGrokReasoningStreamEvent("response.output_item.added", []byte(`{"item":{"type":"message"}}`)))
}

func TestIsGrokOutputDeltaStreamEvent(t *testing.T) {
	require.True(t, isGrokOutputDeltaStreamEvent("response.output_text.delta"))
	require.True(t, isGrokOutputDeltaStreamEvent("response.function_call_arguments.delta"))
	require.False(t, isGrokOutputDeltaStreamEvent("response.created"))
}
