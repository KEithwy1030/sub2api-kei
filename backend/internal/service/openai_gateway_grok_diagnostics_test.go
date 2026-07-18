package service

import (
	"context"
	"testing"

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
