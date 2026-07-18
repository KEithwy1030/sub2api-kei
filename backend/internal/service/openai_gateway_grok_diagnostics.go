package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

type grokPromptCacheDiagnosticContextKey struct{}

type grokPromptCacheDiagnostic struct {
	present     bool
	fingerprint string
}

type grokStreamDiagnostics struct {
	enabled              bool
	ctx                  context.Context
	account              *Account
	model                string
	resp                 *http.Response
	startTime            time.Time
	firstEventSeen       bool
	firstReasoningSeen   bool
	firstOutputDeltaSeen bool
}

func newGrokStreamDiagnostics(ctx context.Context, account *Account, model string, resp *http.Response, startTime time.Time) *grokStreamDiagnostics {
	return &grokStreamDiagnostics{
		enabled:   account != nil && account.Platform == PlatformGrok,
		ctx:       ctx,
		account:   account,
		model:     model,
		resp:      resp,
		startTime: startTime,
	}
}

// GrokSessionDiagnosticFingerprint returns a short irreversible identifier for
// correlating scheduling decisions. It never exposes the session hash itself.
func GrokSessionDiagnosticFingerprint(sessionHash string) string {
	return grokDiagnosticFingerprint("session", sessionHash)
}

func withGrokPromptCacheDiagnostic(ctx context.Context, body []byte) context.Context {
	if ctx == nil {
		return nil
	}
	rawKey := strings.TrimSpace(gjson.GetBytes(body, "prompt_cache_key").String())
	diagnostic := grokPromptCacheDiagnostic{present: rawKey != ""}
	if diagnostic.present {
		diagnostic.fingerprint = grokDiagnosticFingerprint("prompt-cache", rawKey)
	}
	return context.WithValue(ctx, grokPromptCacheDiagnosticContextKey{}, diagnostic)
}

func grokPromptCacheDiagnosticFromContext(ctx context.Context) grokPromptCacheDiagnostic {
	if ctx == nil {
		return grokPromptCacheDiagnostic{}
	}
	diagnostic, _ := ctx.Value(grokPromptCacheDiagnosticContextKey{}).(grokPromptCacheDiagnostic)
	return diagnostic
}

func grokDiagnosticFingerprint(namespace, value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(strings.TrimSpace(namespace) + ":v1:" + value))
	return hex.EncodeToString(sum[:8])
}

func (d *grokStreamDiagnostics) Observe(eventType string, payload []byte) {
	if d == nil || !d.enabled {
		return
	}

	eventType = strings.TrimSpace(eventType)
	if !d.firstEventSeen {
		d.firstEventSeen = true
		logGrokStreamStage(d.ctx, d.account, d.model, d.resp, "first_sse_event", eventType, time.Since(d.startTime), -1)
	}
	if !d.firstReasoningSeen && isGrokReasoningStreamEvent(eventType, payload) {
		d.firstReasoningSeen = true
		logGrokStreamStage(d.ctx, d.account, d.model, d.resp, "first_reasoning_event", eventType, time.Since(d.startTime), -1)
	}
	if !d.firstOutputDeltaSeen && isGrokOutputDeltaStreamEvent(eventType) {
		d.firstOutputDeltaSeen = true
		logGrokStreamStage(d.ctx, d.account, d.model, d.resp, "first_text_tool_delta", eventType, time.Since(d.startTime), -1)
	}
}

func isGrokReasoningStreamEvent(eventType string, payload []byte) bool {
	switch strings.TrimSpace(eventType) {
	case "response.reasoning_summary_part.added",
		"response.reasoning_summary_text.delta",
		"response.reasoning_summary_text.done",
		"response.reasoning_text.delta":
		return true
	case "response.output_item.added", "response.output_item.done":
		return strings.EqualFold(strings.TrimSpace(gjson.GetBytes(payload, "item.type").String()), "reasoning")
	default:
		return false
	}
}

func isGrokOutputDeltaStreamEvent(eventType string) bool {
	switch strings.TrimSpace(eventType) {
	case "response.output_text.delta", "response.function_call_arguments.delta":
		return true
	default:
		return false
	}
}

func logGrokStreamStage(ctx context.Context, account *Account, model string, resp *http.Response, stage, eventType string, elapsed, upstreamHTTPElapsed time.Duration) {
	if account == nil || account.Platform != PlatformGrok {
		return
	}

	requestID := ""
	clientRequestID := ""
	if ctx != nil {
		requestID, _ = ctx.Value(ctxkey.RequestID).(string)
		clientRequestID, _ = ctx.Value(ctxkey.ClientRequestID).(string)
	}
	upstreamRequestID := ""
	if resp != nil {
		upstreamRequestID = firstNonEmpty(resp.Header.Get("x-request-id"), resp.Header.Get("xai-request-id"))
	}
	fields := []zap.Field{
		zap.String("stage", strings.TrimSpace(stage)),
		zap.Int64("account_id", account.ID),
		zap.String("model", strings.TrimSpace(model)),
		zap.Int64("elapsed_ms", elapsed.Milliseconds()),
		zap.String("event_type", strings.TrimSpace(eventType)),
		zap.String("request_id", strings.TrimSpace(requestID)),
		zap.String("client_request_id", strings.TrimSpace(clientRequestID)),
		zap.String("upstream_request_id", strings.TrimSpace(upstreamRequestID)),
	}
	cacheDiagnostic := grokPromptCacheDiagnosticFromContext(ctx)
	fields = append(fields,
		zap.Bool("cache_key_present", cacheDiagnostic.present),
		zap.String("cache_key_fingerprint", cacheDiagnostic.fingerprint),
	)
	if upstreamHTTPElapsed >= 0 {
		fields = append(fields, zap.Int64("upstream_http_ms", upstreamHTTPElapsed.Milliseconds()))
	}
	logger.L().Info("grok stream stage", fields...)
}
