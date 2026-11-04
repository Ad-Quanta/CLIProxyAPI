package handlers

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

type fallbackCaptureExecutor struct {
	mu       sync.Mutex
	requests []coreexecutor.Request
	payloads [][]byte

	execute func(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error)
	stream  func(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error)
}

func (e *fallbackCaptureExecutor) Identifier() string { return "fallback-provider" }

func (e *fallbackCaptureExecutor) record(req coreexecutor.Request) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.requests = append(e.requests, coreexecutor.Request{Model: req.Model, Payload: cloneBytes(req.Payload)})
	e.payloads = append(e.payloads, cloneBytes(req.Payload))
}

func (e *fallbackCaptureExecutor) requestModels() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	models := make([]string, 0, len(e.requests))
	for _, req := range e.requests {
		models = append(models, req.Model)
	}
	return models
}

func (e *fallbackCaptureExecutor) recordedPayloads() [][]byte {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([][]byte, len(e.payloads))
	for i := range e.payloads {
		out[i] = cloneBytes(e.payloads[i])
	}
	return out
}

func (e *fallbackCaptureExecutor) Execute(ctx context.Context, auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
	e.record(req)
	if e.execute != nil {
		return e.execute(ctx, auth, req, opts)
	}
	return coreexecutor.Response{Payload: []byte(fmt.Sprintf(`{"model":%q,"ok":true}`, req.Model))}, nil
}

func (e *fallbackCaptureExecutor) ExecuteStream(ctx context.Context, auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	e.record(req)
	if e.stream != nil {
		return e.stream(ctx, auth, req, opts)
	}
	return streamResultWithPayloads(fmt.Sprintf("data: {\"model\":%q,\"ok\":true}\n\n", req.Model)), nil
}

func (e *fallbackCaptureExecutor) Refresh(context.Context, *coreauth.Auth) (*coreauth.Auth, error) {
	return nil, nil
}

func (e *fallbackCaptureExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, nil
}

func (e *fallbackCaptureExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func streamResultWithPayloads(payloads ...string) *coreexecutor.StreamResult {
	chunks := make(chan coreexecutor.StreamChunk, len(payloads))
	for _, payload := range payloads {
		chunks <- coreexecutor.StreamChunk{Payload: []byte(payload)}
	}
	close(chunks)
	return &coreexecutor.StreamResult{Chunks: chunks}
}

func fallbackTestConfig(from string, targets ...string) *sdkconfig.SDKConfig {
	return &sdkconfig.SDKConfig{
		ModelFallback: sdkconfig.ModelFallback{
			Enabled:               true,
			TriggerStatusCodes:    []int{http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout},
			NoFallbackStatusCodes: []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden, http.StatusUnprocessableEntity},
			Rules:                 []sdkconfig.ModelFallbackRule{{From: from, To: targets}},
		},
	}
}

func newFallbackTestHandler(t *testing.T, cfg *sdkconfig.SDKConfig, executor *fallbackCaptureExecutor, models ...string) *BaseAPIHandler {
	t.Helper()
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	auth := &coreauth.Auth{
		ID:       "model-fallback-auth",
		Provider: executor.Identifier(),
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{"email": "model-fallback@example.com"},
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("manager.Register(): %v", errRegister)
	}
	reg := registry.GetGlobalRegistry()
	infos := make([]*registry.ModelInfo, 0, len(models))
	for _, model := range models {
		infos = append(infos, &registry.ModelInfo{ID: model})
	}
	// The auth manager selects credentials by checking the registry entry whose
	// client ID equals the auth ID, so register every fallback model under it.
	reg.RegisterClient(auth.ID, executor.Identifier(), infos)
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})
	return NewBaseAPIHandlers(cfg, manager)
}

func drainFallbackStream(t *testing.T, dataChan <-chan []byte, errChan <-chan *interfaces.ErrorMessage) (string, *interfaces.ErrorMessage) {
	t.Helper()
	var body strings.Builder
	var errMsg *interfaces.ErrorMessage
	for dataChan != nil || errChan != nil {
		select {
		case payload, ok := <-dataChan:
			if !ok {
				dataChan = nil
				continue
			}
			body.Write(payload)
		case msg, ok := <-errChan:
			if !ok {
				errChan = nil
				continue
			}
			if msg != nil {
				errMsg = msg
			}
		}
	}
	return body.String(), errMsg
}

func TestModelFallbackNonStreamSwitchesModel(t *testing.T) {
	const (
		primary = "fallback-nonstream-primary"
		target  = "fallback-nonstream-target"
	)
	requestBody := []byte(fmt.Sprintf(`{"model":%q,"messages":[]}`, primary))
	executor := &fallbackCaptureExecutor{
		execute: func(_ context.Context, _ *coreauth.Auth, req coreexecutor.Request, _ coreexecutor.Options) (coreexecutor.Response, error) {
			if req.Model == primary {
				return coreexecutor.Response{}, modelExecutionStatusHeaderError{statusCode: http.StatusTooManyRequests, message: "rate limited"}
			}
			return coreexecutor.Response{Payload: []byte(fmt.Sprintf(`{"model":%q,"ok":true}`, target))}, nil
		},
	}
	handler := newFallbackTestHandler(t, fallbackTestConfig(primary, target), executor, primary, target)

	body, _, errMsg := handler.ExecuteWithAuthManager(context.Background(), "openai", primary, requestBody, "")
	if errMsg != nil {
		t.Fatalf("ExecuteWithAuthManager() error = %+v", errMsg)
	}
	if got, want := string(body), fmt.Sprintf(`{"model":%q,"ok":true}`, primary); got != want {
		t.Fatalf("response body = %s, want %s", got, want)
	}
	models := executor.requestModels()
	if len(models) != 2 || models[0] != primary || models[1] != target {
		t.Fatalf("executor models = %v, want [%s %s]", models, primary, target)
	}
	for _, payload := range executor.recordedPayloads() {
		if string(payload) != string(requestBody) {
			t.Fatalf("executor payload = %s, want original client body %s", payload, requestBody)
		}
	}
}

func TestModelFallbackPreserveRequestedModelDisabled(t *testing.T) {
	const (
		primary = "fallback-preserve-off-primary"
		target  = "fallback-preserve-off-target"
	)
	preserve := false
	cfg := fallbackTestConfig(primary, target)
	cfg.ModelFallback.PreserveRequestedModel = &preserve
	executor := &fallbackCaptureExecutor{
		execute: func(_ context.Context, _ *coreauth.Auth, req coreexecutor.Request, _ coreexecutor.Options) (coreexecutor.Response, error) {
			if req.Model == primary {
				return coreexecutor.Response{}, modelExecutionStatusHeaderError{statusCode: http.StatusServiceUnavailable, message: "unavailable"}
			}
			return coreexecutor.Response{Payload: []byte(fmt.Sprintf(`{"model":%q,"ok":true}`, target))}, nil
		},
	}
	handler := newFallbackTestHandler(t, cfg, executor, primary, target)

	body, _, errMsg := handler.ExecuteWithAuthManager(context.Background(), "openai", primary, []byte(fmt.Sprintf(`{"model":%q}`, primary)), "")
	if errMsg != nil {
		t.Fatalf("ExecuteWithAuthManager() error = %+v", errMsg)
	}
	if got, want := string(body), fmt.Sprintf(`{"model":%q,"ok":true}`, target); got != want {
		t.Fatalf("response body = %s, want %s", got, want)
	}
}

func TestModelFallbackNonStreamSkipsNonFallbackStatus(t *testing.T) {
	const (
		primary = "fallback-status-primary"
		target  = "fallback-status-target"
	)
	executor := &fallbackCaptureExecutor{
		execute: func(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
			return coreexecutor.Response{}, modelExecutionStatusHeaderError{statusCode: http.StatusBadRequest, message: "invalid request"}
		},
	}
	handler := newFallbackTestHandler(t, fallbackTestConfig(primary, target), executor, primary, target)

	_, _, errMsg := handler.ExecuteWithAuthManager(context.Background(), "openai", primary, []byte(fmt.Sprintf(`{"model":%q}`, primary)), "")
	if errMsg == nil || errMsg.StatusCode != http.StatusBadRequest {
		t.Fatalf("error = %+v, want status %d", errMsg, http.StatusBadRequest)
	}
	if models := executor.requestModels(); len(models) != 1 || models[0] != primary {
		t.Fatalf("executor models = %v, want only [%s]", models, primary)
	}
}

func TestModelFallbackNonStreamChainStopsAtEnd(t *testing.T) {
	const (
		primary = "fallback-chain-primary"
		target1 = "fallback-chain-target-1"
		target2 = "fallback-chain-target-2"
	)
	executor := &fallbackCaptureExecutor{
		execute: func(_ context.Context, _ *coreauth.Auth, req coreexecutor.Request, _ coreexecutor.Options) (coreexecutor.Response, error) {
			if req.Model == target2 {
				return coreexecutor.Response{Payload: []byte(fmt.Sprintf(`{"model":%q,"ok":true}`, target2))}, nil
			}
			return coreexecutor.Response{}, modelExecutionStatusHeaderError{statusCode: http.StatusTooManyRequests, message: "rate limited"}
		},
	}
	handler := newFallbackTestHandler(t, fallbackTestConfig(primary, target1, target2), executor, primary, target1, target2)

	body, _, errMsg := handler.ExecuteWithAuthManager(context.Background(), "openai", primary, []byte(fmt.Sprintf(`{"model":%q}`, primary)), "")
	if errMsg != nil {
		t.Fatalf("ExecuteWithAuthManager() error = %+v", errMsg)
	}
	if !strings.Contains(string(body), fmt.Sprintf(`"model":%q`, primary)) {
		t.Fatalf("response body = %s, want model %q", body, primary)
	}
	models := executor.requestModels()
	want := []string{primary, target1, target2}
	if len(models) != len(want) {
		t.Fatalf("executor models = %v, want %v", models, want)
	}
	for i := range want {
		if models[i] != want[i] {
			t.Fatalf("executor models = %v, want %v", models, want)
		}
	}
}

func TestModelFallbackStreamBeforeFirstByte(t *testing.T) {
	const (
		primary = "fallback-stream-primary"
		target  = "fallback-stream-target"
	)
	executor := &fallbackCaptureExecutor{
		stream: func(_ context.Context, _ *coreauth.Auth, req coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
			if req.Model == primary {
				chunks := make(chan coreexecutor.StreamChunk, 1)
				chunks <- coreexecutor.StreamChunk{Err: modelExecutionStatusHeaderError{statusCode: http.StatusTooManyRequests, message: "rate limited"}}
				close(chunks)
				return &coreexecutor.StreamResult{Chunks: chunks}, nil
			}
			return streamResultWithPayloads(fmt.Sprintf("data: {\"model\":%q,\"ok\":true}\n\n", target)), nil
		},
	}
	handler := newFallbackTestHandler(t, fallbackTestConfig(primary, target), executor, primary, target)

	dataChan, _, errChan := handler.ExecuteStreamWithAuthManager(context.Background(), "openai", primary, []byte(fmt.Sprintf(`{"model":%q,"stream":true}`, primary)), "")
	body, errMsg := drainFallbackStream(t, dataChan, errChan)
	if errMsg != nil {
		t.Fatalf("stream error = %+v", errMsg)
	}
	if !strings.Contains(body, fmt.Sprintf(`"model":%q`, primary)) {
		t.Fatalf("stream body = %q, want model %q", body, primary)
	}
	models := executor.requestModels()
	if len(models) != 2 || models[0] != primary || models[1] != target {
		t.Fatalf("executor models = %v, want [%s %s]", models, primary, target)
	}
}

func TestModelFallbackStreamSynchronousError(t *testing.T) {
	const (
		primary = "fallback-stream-sync-primary"
		target  = "fallback-stream-sync-target"
	)
	executor := &fallbackCaptureExecutor{
		stream: func(_ context.Context, _ *coreauth.Auth, req coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
			if req.Model == primary {
				return nil, &coreauth.Error{Code: "stream_failed", Message: "upstream failed", HTTPStatus: http.StatusServiceUnavailable}
			}
			return streamResultWithPayloads(fmt.Sprintf("data: {\"model\":%q,\"ok\":true}\n\n", target)), nil
		},
	}
	handler := newFallbackTestHandler(t, fallbackTestConfig(primary, target), executor, primary, target)

	dataChan, _, errChan := handler.ExecuteStreamWithAuthManager(context.Background(), "openai", primary, []byte(fmt.Sprintf(`{"model":%q,"stream":true}`, primary)), "")
	body, errMsg := drainFallbackStream(t, dataChan, errChan)
	if errMsg != nil {
		t.Fatalf("stream error = %+v", errMsg)
	}
	if !strings.Contains(body, fmt.Sprintf(`"model":%q`, primary)) {
		t.Fatalf("stream body = %q, want model %q", body, primary)
	}
	models := executor.requestModels()
	if len(models) != 2 || models[0] != primary || models[1] != target {
		t.Fatalf("executor models = %v, want [%s %s]", models, primary, target)
	}
}

func TestModelFallbackStreamAfterFirstByteDoesNotSwitch(t *testing.T) {
	const (
		primary = "fallback-stream-late-primary"
		target  = "fallback-stream-late-target"
	)
	executor := &fallbackCaptureExecutor{
		stream: func(_ context.Context, _ *coreauth.Auth, req coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
			if req.Model == primary {
				chunks := make(chan coreexecutor.StreamChunk, 2)
				chunks <- coreexecutor.StreamChunk{Payload: []byte(fmt.Sprintf("data: {\"model\":%q,\"ok\":true}\n\n", primary))}
				chunks <- coreexecutor.StreamChunk{Err: modelExecutionStatusHeaderError{statusCode: http.StatusTooManyRequests, message: "rate limited"}}
				close(chunks)
				return &coreexecutor.StreamResult{Chunks: chunks}, nil
			}
			return streamResultWithPayloads(fmt.Sprintf("data: {\"model\":%q,\"ok\":true}\n\n", target)), nil
		},
	}
	handler := newFallbackTestHandler(t, fallbackTestConfig(primary, target), executor, primary, target)

	dataChan, _, errChan := handler.ExecuteStreamWithAuthManager(context.Background(), "openai", primary, []byte(fmt.Sprintf(`{"model":%q,"stream":true}`, primary)), "")
	body, errMsg := drainFallbackStream(t, dataChan, errChan)
	if !strings.Contains(body, fmt.Sprintf(`"model":%q`, primary)) {
		t.Fatalf("stream body = %q, want first-byte payload for %q", body, primary)
	}
	if errMsg == nil || errMsg.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("stream error = %+v, want status %d", errMsg, http.StatusTooManyRequests)
	}
	if models := executor.requestModels(); len(models) != 1 || models[0] != primary {
		t.Fatalf("executor models = %v, want only [%s]", models, primary)
	}
}

func TestModelFallbackStreamBootstrapRetryThenFallback(t *testing.T) {
	const (
		primary = "fallback-stream-retry-primary"
		target  = "fallback-stream-retry-target"
	)
	cfg := fallbackTestConfig(primary, target)
	cfg.Streaming.BootstrapRetries = 1
	executor := &fallbackCaptureExecutor{
		stream: func(_ context.Context, _ *coreauth.Auth, req coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
			if req.Model == primary {
				chunks := make(chan coreexecutor.StreamChunk, 1)
				chunks <- coreexecutor.StreamChunk{Err: modelExecutionStatusHeaderError{statusCode: http.StatusServiceUnavailable, message: "unavailable"}}
				close(chunks)
				return &coreexecutor.StreamResult{Chunks: chunks}, nil
			}
			return streamResultWithPayloads(fmt.Sprintf("data: {\"model\":%q,\"ok\":true}\n\n", target)), nil
		},
	}
	handler := newFallbackTestHandler(t, cfg, executor, primary, target)
	// The first 503 cools the primary credential for that model, so a second
	// credential is required for the same-model bootstrap retry.
	retryAuth := &coreauth.Auth{
		ID:       "model-fallback-auth-retry",
		Provider: executor.Identifier(),
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{"email": "model-fallback-retry@example.com"},
	}
	if _, errRegister := handler.AuthManager.Register(context.Background(), retryAuth); errRegister != nil {
		t.Fatalf("manager.Register(retry): %v", errRegister)
	}
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(retryAuth.ID, retryAuth.Provider, []*registry.ModelInfo{{ID: primary}, {ID: target}})
	t.Cleanup(func() {
		reg.UnregisterClient(retryAuth.ID)
	})

	dataChan, _, errChan := handler.ExecuteStreamWithAuthManager(context.Background(), "openai", primary, []byte(fmt.Sprintf(`{"model":%q,"stream":true}`, primary)), "")
	body, errMsg := drainFallbackStream(t, dataChan, errChan)
	if errMsg != nil {
		t.Fatalf("stream error = %+v", errMsg)
	}
	if !strings.Contains(body, fmt.Sprintf(`"model":%q`, primary)) {
		t.Fatalf("stream body = %q, want model %q", body, primary)
	}
	models := executor.requestModels()
	want := []string{primary, primary, target}
	if len(models) != len(want) {
		t.Fatalf("executor models = %v, want %v", models, want)
	}
	for i := range want {
		if models[i] != want[i] {
			t.Fatalf("executor models = %v, want %v", models, want)
		}
	}
}

type fallbackRouterHost struct {
	route func(context.Context, pluginapi.ModelRouteRequest) pluginapi.ModelRouteResponse
}

func (h *fallbackRouterHost) RouteModel(ctx context.Context, req pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, bool) {
	return h.route(ctx, req), true
}

func (h *fallbackRouterHost) HasModelRouters() bool { return true }

func TestModelFallbackRouterProviderUsesFallbackModel(t *testing.T) {
	const (
		primary = "fallback-router-primary"
		target  = "fallback-router-target"
	)
	executor := &fallbackCaptureExecutor{
		execute: func(_ context.Context, _ *coreauth.Auth, req coreexecutor.Request, _ coreexecutor.Options) (coreexecutor.Response, error) {
			if req.Model == primary {
				return coreexecutor.Response{}, modelExecutionStatusHeaderError{statusCode: http.StatusServiceUnavailable, message: "unavailable"}
			}
			return coreexecutor.Response{Payload: []byte(fmt.Sprintf(`{"model":%q,"ok":true}`, req.Model))}, nil
		},
	}
	handler := newFallbackTestHandler(t, fallbackTestConfig(primary, target), executor, primary, target)
	handler.SetModelRouterHost(&fallbackRouterHost{
		route: func(context.Context, pluginapi.ModelRouteRequest) pluginapi.ModelRouteResponse {
			return pluginapi.ModelRouteResponse{
				Handled:    true,
				TargetKind: pluginapi.ModelRouteTargetProvider,
				Target:     executor.Identifier(),
			}
		},
	})

	body, _, errMsg := handler.ExecuteWithAuthManager(context.Background(), "openai", primary, []byte(fmt.Sprintf(`{"model":%q}`, primary)), "")
	if errMsg != nil {
		t.Fatalf("ExecuteWithAuthManager() error = %+v", errMsg)
	}
	if !strings.Contains(string(body), fmt.Sprintf(`"model":%q`, primary)) {
		t.Fatalf("response body = %s, want model %q", body, primary)
	}
	models := executor.requestModels()
	if len(models) != 2 || models[0] != primary || models[1] != target {
		t.Fatalf("executor models = %v, want [%s %s]", models, primary, target)
	}
}

func TestModelFallbackDisabledKeepsRoutedModel(t *testing.T) {
	const (
		requested = "fallback-disabled-requested"
		routed    = "fallback-disabled-routed"
	)
	executor := &fallbackCaptureExecutor{
		execute: func(_ context.Context, _ *coreauth.Auth, req coreexecutor.Request, _ coreexecutor.Options) (coreexecutor.Response, error) {
			return coreexecutor.Response{Payload: []byte(fmt.Sprintf(`{"model":%q,"ok":true}`, req.Model))}, nil
		},
	}
	handler := newFallbackTestHandler(t, &sdkconfig.SDKConfig{}, executor, requested, routed)
	handler.SetModelRouterHost(&fallbackRouterHost{
		route: func(context.Context, pluginapi.ModelRouteRequest) pluginapi.ModelRouteResponse {
			return pluginapi.ModelRouteResponse{
				Handled:     true,
				TargetKind:  pluginapi.ModelRouteTargetProvider,
				Target:      executor.Identifier(),
				TargetModel: routed,
			}
		},
	})

	body, _, errMsg := handler.ExecuteWithAuthManager(context.Background(), "openai", requested, []byte(fmt.Sprintf(`{"model":%q}`, requested)), "")
	if errMsg != nil {
		t.Fatalf("ExecuteWithAuthManager() error = %+v", errMsg)
	}
	if got, want := string(body), fmt.Sprintf(`{"model":%q,"ok":true}`, routed); got != want {
		t.Fatalf("response body = %s, want routed model %s", got, want)
	}
	if models := executor.requestModels(); len(models) != 1 || models[0] != routed {
		t.Fatalf("executor models = %v, want only [%s]", models, routed)
	}
}

func TestModelFallbackDisabledKeepsRoutedStreamModel(t *testing.T) {
	const (
		requested = "fallback-disabled-stream-requested"
		routed    = "fallback-disabled-stream-routed"
	)
	executor := &fallbackCaptureExecutor{
		stream: func(_ context.Context, _ *coreauth.Auth, req coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
			return streamResultWithPayloads(fmt.Sprintf("data: {\"model\":%q,\"ok\":true}\n\n", req.Model)), nil
		},
	}
	handler := newFallbackTestHandler(t, &sdkconfig.SDKConfig{}, executor, requested, routed)
	handler.SetModelRouterHost(&fallbackRouterHost{
		route: func(context.Context, pluginapi.ModelRouteRequest) pluginapi.ModelRouteResponse {
			return pluginapi.ModelRouteResponse{
				Handled:     true,
				TargetKind:  pluginapi.ModelRouteTargetProvider,
				Target:      executor.Identifier(),
				TargetModel: routed,
			}
		},
	})

	dataChan, _, errChan := handler.ExecuteStreamWithAuthManager(context.Background(), "openai", requested, []byte(fmt.Sprintf(`{"model":%q,"stream":true}`, requested)), "")
	body, errMsg := drainFallbackStream(t, dataChan, errChan)
	if errMsg != nil {
		t.Fatalf("stream error = %+v", errMsg)
	}
	if got, want := body, fmt.Sprintf("data: {\"model\":%q,\"ok\":true}\n\n", routed); got != want {
		t.Fatalf("stream body = %q, want routed model %q", got, want)
	}
	if models := executor.requestModels(); len(models) != 1 || models[0] != routed {
		t.Fatalf("executor models = %v, want only [%s]", models, routed)
	}
}
