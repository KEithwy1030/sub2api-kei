package service

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

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
	if upstreamHTTPElapsed >= 0 {
		fields = append(fields, zap.Int64("upstream_http_ms", upstreamHTTPElapsed.Milliseconds()))
	}
	logger.L().Info("grok stream stage", fields...)
}
