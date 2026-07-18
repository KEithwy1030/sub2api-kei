package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

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
