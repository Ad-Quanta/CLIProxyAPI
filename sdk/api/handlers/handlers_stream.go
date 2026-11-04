package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"golang.org/x/net/context"
)

// ExecuteStreamWithAuthManager executes a streaming request via the core auth manager.
// This path is the only supported execution route.
// The returned http.Header carries upstream response headers captured before streaming begins.
func (h *BaseAPIHandler) ExecuteStreamWithAuthManager(ctx context.Context, handlerType, modelName string, rawJSON []byte, alt string) (<-chan []byte, http.Header, <-chan *interfaces.ErrorMessage) {
	return h.executeStreamWithAuthManager(ctx, handlerType, modelName, rawJSON, alt, false)
}

// ExecuteImageStreamWithAuthManager executes a streaming OpenAI-compatible image endpoint request.
func (h *BaseAPIHandler) ExecuteImageStreamWithAuthManager(ctx context.Context, handlerType, modelName string, rawJSON []byte, alt string) (<-chan []byte, http.Header, <-chan *interfaces.ErrorMessage) {
	return h.executeStreamWithAuthManager(ctx, handlerType, modelName, rawJSON, alt, true)
}

func (h *BaseAPIHandler) streamWithPluginExecutor(ctx context.Context, entryProtocol, responseProtocol, modelName, originalRequestedModel string, rawJSON []byte, alt, executorPluginID string, execOptions modelExecutionOptions) (<-chan []byte, http.Header, <-chan *interfaces.ErrorMessage) {
	if h.AuthManager != nil && h.AuthManager.HomeEnabled() {
		errChan := make(chan *interfaces.ErrorMessage, 1)
		errChan <- &interfaces.ErrorMessage{StatusCode: http.StatusServiceUnavailable, Error: fmt.Errorf("plugin executor routing is unavailable while Home is enabled")}
		close(errChan)
		return nil, nil, errChan
	}
	host := h.pluginExecutorHost()
	if host == nil {
		errChan := make(chan *interfaces.ErrorMessage, 1)
		errChan <- &interfaces.ErrorMessage{StatusCode: http.StatusBadGateway, Error: fmt.Errorf("plugin executor host is unavailable")}
		close(errChan)
		return nil, nil, errChan
	}
	execCtx, nestedTracker := withNestedExecutionTracker(coreusage.WithStream(ctx, true))
	req, opts := h.pluginExecutorRequest(execCtx, entryProtocol, responseProtocol, modelName, originalRequestedModel, rawJSON, alt, true, execOptions)
	lifecycle := h.newRequestLifecycleTracker(execCtx, entryProtocol, modelName, originalRequestedModel, true, opts.Metadata, execOptions.SkipInterceptorPluginID)
	var interceptErr *interfaces.ErrorMessage
	req, opts, interceptErr = h.applyRequestInterceptorsBeforeAuth(execCtx, entryProtocol, originalRequestedModel, lifecycle.requestID(), req, opts, execOptions.SkipInterceptorPluginID)
	if interceptErr != nil {
		lifecycle.completeError(execCtx, interceptErr)
		errChan := make(chan *interfaces.ErrorMessage, 1)
		errChan <- interceptErr
		close(errChan)
		return nil, nil, errChan
	}
	req, opts, interceptErr = h.applyRequestInterceptorsAfterPluginExecutorRoute(execCtx, host, executorPluginID, entryProtocol, originalRequestedModel, lifecycle.requestID(), req, opts, execOptions.SkipInterceptorPluginID)
	if interceptErr != nil {
		lifecycle.completeError(execCtx, interceptErr)
		errChan := make(chan *interfaces.ErrorMessage, 1)
		errChan <- interceptErr
		close(errChan)
		return nil, nil, errChan
	}
	execCtx = enrichContextWithSessionHierarchy(execCtx, opts.Headers, req.Payload, opts.Metadata)
	var reporter *helps.UsageReporter
	if !execOptions.InternalSource {
		reporter = helps.NewUsageReporter(execCtx, executorPluginID, modelName, nil)
		reporter.SetTranslatedReasoningEffort(req.Payload, entryProtocol)
	}
	streamResult, errStream := host.ExecutePluginExecutorStream(execCtx, executorPluginID, req, opts)
	if errStream != nil {
		if reporter != nil && !nestedTracker.hasNestedExecution() {
			reporter.PublishFailure(execCtx, errStream)
		}
		errMsg := executionErrorMessage(errStream)
		lifecycle.completeError(execCtx, errMsg)
		errChan := make(chan *interfaces.ErrorMessage, 1)
		errChan <- errMsg
		close(errChan)
		return nil, nil, errChan
	}
	if streamResult == nil {
		errMsg := &interfaces.ErrorMessage{StatusCode: http.StatusBadGateway, Error: fmt.Errorf("plugin executor returned nil stream")}
		if reporter != nil && !nestedTracker.hasNestedExecution() {
			reporter.PublishFailure(execCtx, errMsg.Error)
		}
		lifecycle.completeError(execCtx, errMsg)
		errChan := make(chan *interfaces.ErrorMessage, 1)
		errChan <- errMsg
		close(errChan)
		return nil, nil, errChan
	}

	passthroughHeadersEnabled := executionPassthroughHeaders(h.CurrentConfig(), execOptions.InternalSource)
	interceptorHost := h.interceptorHost()
	streamInterceptorsActive := streamInterceptorsEnabled(interceptorHost)
	rawStreamHeaders := cloneHeader(streamResult.Headers)
	baseStreamHeaders := cloneHeader(streamResult.Headers)
	// Request headers and request bodies are stream-invariant. Keep a private snapshot
	// and clone into each interceptor call so plugins cannot mutate shared storage.
	// Schema v3+ payload chunks omit these bodies (host also strips per plugin).
	var streamRequestHeaders http.Header
	var streamOriginalRequest []byte
	var streamRequestBody []byte
	applyStreamHeaders := func(headers http.Header) {
		rawStreamHeaders = finalInterceptorHeaders(rawStreamHeaders, headers)
	}
	if streamInterceptorsActive {
		streamRequestHeaders = cloneHeader(opts.Headers)
		streamOriginalRequest = cloneBytes(opts.OriginalRequest)
		streamRequestBody = cloneBytes(req.Payload)
		intercepted := interceptStreamChunk(ctx, interceptorHost, pluginapi.StreamChunkInterceptRequest{
			RequestID:       lifecycle.requestID(),
			SourceFormat:    responseProtocol,
			Model:           modelName,
			RequestedModel:  originalRequestedModel,
			RequestHeaders:  cloneHeader(streamRequestHeaders),
			ResponseHeaders: cloneHeader(rawStreamHeaders),
			OriginalRequest: cloneBytes(streamOriginalRequest),
			RequestBody:     cloneBytes(streamRequestBody),
			ChunkIndex:      pluginapi.StreamChunkHeaderInitIndex,
			Metadata:        opts.Metadata,
		}, execOptions.SkipInterceptorPluginID)
		applyStreamHeaders(intercepted.Headers)
	}
	upstreamHeaders := downstreamHeadersAfterInterceptors(baseStreamHeaders, rawStreamHeaders, passthroughHeadersEnabled)
	if upstreamHeaders == nil && (passthroughHeadersEnabled || streamInterceptorsActive) {
		upstreamHeaders = make(http.Header)
	}

	dataChan := make(chan []byte)
	errChan := make(chan *interfaces.ErrorMessage, 1)
	var done <-chan struct{}
	if ctx != nil {
		done = ctx.Done()
	}
	chunks := streamResult.Chunks
	if chunks == nil {
		closed := make(chan coreexecutor.StreamChunk)
		close(closed)
		chunks = closed
	}
	var responseSSEValidator *sseJSONValidationState
	if responseProtocol == "openai-response" {
		responseSSEValidator = &sseJSONValidationState{}
	}
	go func() {
		completionOutcome := pluginapi.RequestCompletionSucceeded
		completionStatus := http.StatusOK
		var completionErr error
		var streamUsage helps.StreamUsageBuffer
		defer func() {
			completionOutcome, completionStatus, completionErr = streamDeliveryCompletion(ctx, completionOutcome, completionStatus, completionErr)
			lifecycle.complete(completionOutcome, completionStatus, completionErr)
			if reporter != nil && !nestedTracker.hasNestedExecution() {
				if completionOutcome != pluginapi.RequestCompletionSucceeded && completionErr != nil {
					if !streamUsage.PublishFailure(execCtx, reporter, completionErr) {
						reporter.PublishFailure(execCtx, completionErr)
					}
				} else {
					streamUsage.Publish(execCtx, reporter)
					reporter.EnsurePublished(execCtx)
				}
			}
		}()
		defer close(dataChan)
		defer close(errChan)
		chunkIndex := 0
		var historyChunks [][]byte
		for {
			chunk, ok, canceled := nextStreamChunk(ctx, nil, nil, chunks)
			if canceled {
				completionOutcome = pluginapi.RequestCompletionCanceled
				completionStatus = 0
				if ctx != nil {
					completionErr = ctx.Err()
				}
				return
			}
			if !ok {
				if responseSSEValidator != nil {
					if errValidate := responseSSEValidator.Finish(); errValidate != nil {
						completionOutcome = pluginapi.RequestCompletionFailed
						completionStatus = http.StatusBadGateway
						completionErr = errValidate
						select {
						case errChan <- &interfaces.ErrorMessage{StatusCode: http.StatusBadGateway, Error: errValidate}:
						case <-done:
						}
					}
				}
				return
			}
			if chunk.Err != nil {
				errMsg := executionErrorMessage(chunk.Err)
				completionOutcome = pluginapi.RequestCompletionFailed
				completionStatus = errMsg.StatusCode
				completionErr = chunk.Err
				select {
				case errChan <- errMsg:
				case <-done:
				}
				return
			}
			if len(chunk.Payload) == 0 {
				continue
			}
			observePluginExecutorStreamUsage(responseProtocol, chunk.Payload, &streamUsage)
			payload := cloneBytes(chunk.Payload)
			if streamInterceptorsActive {
				chunkReq := pluginapi.StreamChunkInterceptRequest{
					RequestID:       lifecycle.requestID(),
					SourceFormat:    responseProtocol,
					Model:           modelName,
					RequestedModel:  originalRequestedModel,
					RequestHeaders:  cloneHeader(streamRequestHeaders),
					ResponseHeaders: cloneHeader(rawStreamHeaders),
					Body:            payload,
					ChunkIndex:      chunkIndex,
					Metadata:        opts.Metadata,
				}
				// Re-evaluate each chunk so mid-stream plugin reloads stay correct.
				// Schema v5+ omits history here.
				if streamChunkPayloadIncludesHistory(interceptorHost) {
					chunkReq.HistoryChunks = cloneByteSlices(historyChunks)
				}
				// Schema v3+ omits bodies here (one header-init clone only).
				if streamChunkPayloadIncludesRequestBody(interceptorHost) {
					chunkReq.OriginalRequest = cloneBytes(streamOriginalRequest)
					chunkReq.RequestBody = cloneBytes(streamRequestBody)
				}
				intercepted := interceptStreamChunk(ctx, interceptorHost, chunkReq, execOptions.SkipInterceptorPluginID)
				applyStreamHeaders(intercepted.Headers)
				if len(intercepted.Body) > 0 {
					payload = cloneBytes(intercepted.Body)
				}
				chunkIndex++
				if intercepted.DropChunk {
					continue
				}
			} else {
				chunkIndex++
			}
			if responseSSEValidator != nil {
				validatedPayload, errValidate := responseSSEValidator.AddChunk(payload)
				if errValidate != nil {
					completionOutcome = pluginapi.RequestCompletionFailed
					completionStatus = http.StatusBadGateway
					completionErr = errValidate
					select {
					case errChan <- &interfaces.ErrorMessage{StatusCode: http.StatusBadGateway, Error: errValidate}:
					case <-done:
					}
					return
				}
				payload = validatedPayload
				if len(payload) == 0 {
					continue
				}
			}
			select {
			case dataChan <- payload:
				if streamInterceptorsActive && streamChunkPayloadIncludesHistory(interceptorHost) {
					historyChunks = appendStreamInterceptorHistory(historyChunks, payload)
				}
			case <-done:
				completionOutcome = pluginapi.RequestCompletionCanceled
				completionStatus = 0
				if ctx != nil {
					completionErr = ctx.Err()
				}
				return
			}
		}
	}()
	return dataChan, upstreamHeaders, errChan
}

func (h *BaseAPIHandler) executeStreamWithAuthManager(ctx context.Context, handlerType, modelName string, rawJSON []byte, alt string, allowImageModel bool) (<-chan []byte, http.Header, <-chan *interfaces.ErrorMessage) {
	return h.executeStreamWithAuthManagerFormats(ctx, handlerType, handlerType, modelName, rawJSON, alt, allowImageModel, modelExecutionOptions{})
}

// streamAttempt holds one resolved upstream attempt for a streaming request.
// Cross-model fallback replaces it before any response byte is delivered.
type streamAttempt struct {
	providers       []string
	normalizedModel string
	req             coreexecutor.Request
	opts            coreexecutor.Options
	capture         *requestAfterAuthCapture
	ctx             context.Context
}

func streamErrorChannels(errMsg *interfaces.ErrorMessage) (<-chan []byte, http.Header, <-chan *interfaces.ErrorMessage) {
	errChan := make(chan *interfaces.ErrorMessage, 1)
	errChan <- errMsg
	close(errChan)
	return nil, nil, errChan
}

func (h *BaseAPIHandler) executeStreamWithAuthManagerFormats(ctx context.Context, entryProtocol, exitProtocol, modelName string, rawJSON []byte, alt string, allowImageModel bool, execOptions modelExecutionOptions) (<-chan []byte, http.Header, <-chan *interfaces.ErrorMessage) {
	originalRequestedModel := modelName
	// Speech-only models are reachable solely through the speech entry protocol.
	execOptions.AllowSpeechModel = isModelExecutionSpeechProtocol(entryProtocol)
	initialRouteDecision, preparedRoute := preparedModelRouteFromContext(ctx, execOptions.SkipRouterPluginID)
	if !preparedRoute {
		initialRouteDecision = h.applyModelRouter(ctx, entryProtocol, modelName, rawJSON, true, execOptions)
	}
	responseProtocol := modelExecutionResponseProtocol(entryProtocol, exitProtocol)
	if errMsg := validateNativeInteractionsExecution(entryProtocol, execOptions, initialRouteDecision); errMsg != nil {
		return streamErrorChannels(errMsg)
	}
	if initialRouteDecision.ExecutorPluginID != "" {
		return h.streamWithPluginExecutor(ctx, entryProtocol, responseProtocol, modelName, originalRequestedModel, rawJSON, alt, initialRouteDecision.ExecutorPluginID, execOptions)
	}

	baseCtx := ctx
	passthroughHeadersEnabled := executionPassthroughHeaders(h.CurrentConfig(), execOptions.InternalSource)
	interceptorHost := h.interceptorHost()
	streamInterceptorsActive := streamInterceptorsEnabled(interceptorHost)

	var lifecycle *requestLifecycleTracker
	var executedRequest func() (coreexecutor.Request, coreexecutor.Options)
	var normalizedModel string
	var currentOpts coreexecutor.Options

	buildStreamAttempt := func(attemptCtx context.Context, attemptModel string, attemptOpts modelExecutionOptions, routeDecision modelRouteDecision) (*streamAttempt, *interfaces.ErrorMessage) {
		providers, attemptModelResolved, errMsg := h.providersForExecution(attemptModel, attemptModel, allowImageModel, routeDecision, attemptOpts)
		if errMsg != nil {
			return nil, errMsg
		}
		providers = adjustExecutionProvidersForEntryProtocol(entryProtocol, providers)
		reqMeta := requestExecutionMetadata(attemptCtx)
		if attemptOpts.Path != "" {
			reqMeta[coreexecutor.RequestPathMetadataKey] = attemptOpts.Path
		}
		addCallerScopeMetadata(reqMeta, attemptOpts.CallerScope)
		reqMeta[coreexecutor.RequestedModelMetadataKey] = originalRequestedModel
		addAuthSelectionModelMetadata(reqMeta, attemptOpts.AuthSelectionModel)
		addModelExecutionSourceMetadata(reqMeta, attemptOpts.InternalSource)
		setReasoningEffortMetadata(reqMeta, entryProtocol, attemptModelResolved, rawJSON)
		setServiceTierMetadata(reqMeta, rawJSON)
		setGenerateMetadata(reqMeta, rawJSON)
		payload := rawJSON
		if len(payload) == 0 {
			payload = nil
		}
		req := coreexecutor.Request{
			Model:   attemptModelResolved,
			Payload: payload,
		}
		originalRequest := rawJSON
		if ctxOriginal := originalRequestFromContext(attemptCtx); len(ctxOriginal) > 0 {
			originalRequest = ctxOriginal
		}
		afterAuthCapture := &requestAfterAuthCapture{}
		if lifecycle == nil {
			lifecycle = h.newRequestLifecycleTracker(attemptCtx, entryProtocol, attemptModelResolved, originalRequestedModel, true, reqMeta, attemptOpts.SkipInterceptorPluginID)
		}
		opts := coreexecutor.Options{
			Stream:                       true,
			Alt:                          alt,
			OriginalRequest:              originalRequest,
			SourceFormat:                 sdktranslator.FromString(entryProtocol),
			ResponseFormat:               sdktranslator.FromString(responseProtocol),
			Headers:                      modelExecutionHeaders(attemptCtx, attemptOpts.Headers),
			Query:                        modelExecutionQuery(attemptCtx, attemptOpts.Query),
			RequestBeforeAuthInterceptor: h.requestBeforeAuthInterceptor(lifecycle.requestID(), attemptOpts.SkipInterceptorPluginID),
			RequestAfterAuthInterceptor:  h.requestAfterAuthInterceptor(afterAuthCapture, lifecycle.requestID(), attemptOpts.SkipInterceptorPluginID),
			WebSocketResponseObserver:    h.webSocketResponseObserver(lifecycle.requestID(), attemptOpts.SkipInterceptorPluginID),
			ProxyURL:                     attemptOpts.ProxyURL,
		}
		opts.Metadata = reqMeta
		attemptCtx = enrichContextWithSessionHierarchy(attemptCtx, opts.Headers, req.Payload, opts.Metadata)
		var interceptErr *interfaces.ErrorMessage
		req, opts, interceptErr = h.applyRequestInterceptorsBeforeAuth(attemptCtx, entryProtocol, originalRequestedModel, lifecycle.requestID(), req, opts, attemptOpts.SkipInterceptorPluginID)
		if interceptErr != nil {
			return nil, interceptErr
		}
		attemptCtx = enrichContextWithSessionHierarchy(attemptCtx, opts.Headers, req.Payload, opts.Metadata)
		return &streamAttempt{
			providers:       providers,
			normalizedModel: attemptModelResolved,
			req:             req,
			opts:            opts,
			capture:         afterAuthCapture,
			ctx:             attemptCtx,
		}, nil
	}

	activateStreamAttempt := func(attempt *streamAttempt) {
		normalizedModel = attempt.normalizedModel
		currentOpts = attempt.opts
		executedRequest = func() (coreexecutor.Request, coreexecutor.Options) {
			return attempt.capture.apply(attempt.req, attempt.opts)
		}
	}

	initialAttempt, errMsg := buildStreamAttempt(baseCtx, modelName, execOptions, initialRouteDecision)
	if errMsg != nil {
		if lifecycle != nil {
			lifecycle.completeError(baseCtx, errMsg)
		}
		return streamErrorChannels(errMsg)
	}
	activateStreamAttempt(initialAttempt)
	attempt := initialAttempt
	attemptModel := modelName
	attemptOpts := execOptions

	// Resolve bootstrap retries and header initialization before returning so the
	// returned header snapshot is never modified by the stream goroutine.
	var rawStreamHeaders http.Header
	var baseStreamHeaders http.Header
	var chunks <-chan coreexecutor.StreamChunk
	streamClosedBeforeRead := false
	streamCanceledBeforeRead := false
	streamHeaderInitialized := false
	// Request headers/bodies are stream-invariant after after-auth capture. Keep a private
	// snapshot and clone into each interceptor call so plugins cannot mutate shared storage.
	// Schema v3+ payload chunks omit these bodies (host also strips per plugin).
	var streamRequestHeaders http.Header
	var streamOriginalRequest []byte
	var streamRequestBody []byte

	applyStreamHeaders := func(headers http.Header) {
		rawStreamHeaders = finalInterceptorHeaders(rawStreamHeaders, headers)
	}

	applyStreamHeaderInit := func() {
		if !streamInterceptorsActive || streamHeaderInitialized {
			return
		}
		executedReq, executedOpts := executedRequest()
		streamRequestHeaders = cloneHeader(executedOpts.Headers)
		streamOriginalRequest = cloneBytes(executedOpts.OriginalRequest)
		streamRequestBody = cloneBytes(executedReq.Payload)
		intercepted := interceptStreamChunk(ctx, interceptorHost, pluginapi.StreamChunkInterceptRequest{
			RequestID:       lifecycle.requestID(),
			SourceFormat:    responseProtocol,
			Model:           normalizedModel,
			RequestedModel:  originalRequestedModel,
			RequestHeaders:  cloneHeader(streamRequestHeaders),
			ResponseHeaders: cloneHeader(rawStreamHeaders),
			OriginalRequest: cloneBytes(streamOriginalRequest),
			RequestBody:     cloneBytes(streamRequestBody),
			ChunkIndex:      pluginapi.StreamChunkHeaderInitIndex,
			Metadata:        executedOpts.Metadata,
		}, execOptions.SkipInterceptorPluginID)
		applyStreamHeaders(intercepted.Headers)
		streamHeaderInitialized = true
	}

	var responseSSEValidator *sseJSONValidationState
	if responseProtocol == "openai-response" {
		responseSSEValidator = &sseJSONValidationState{}
	}

	transformStreamPayload := func(payload []byte, chunkIndex *int, historyChunks [][]byte) ([]byte, bool, *interfaces.ErrorMessage) {
		applyStreamHeaderInit()
		payload = cloneBytes(payload)
		if streamInterceptorsActive {
			chunkReq := pluginapi.StreamChunkInterceptRequest{
				RequestID:       lifecycle.requestID(),
				SourceFormat:    responseProtocol,
				Model:           normalizedModel,
				RequestedModel:  originalRequestedModel,
				RequestHeaders:  cloneHeader(streamRequestHeaders),
				ResponseHeaders: cloneHeader(rawStreamHeaders),
				Body:            payload,
				ChunkIndex:      *chunkIndex,
				Metadata:        currentOpts.Metadata,
			}
			// Re-evaluate each chunk so mid-stream plugin reloads stay correct.
			// Schema v5+ omits history here.
			if streamChunkPayloadIncludesHistory(interceptorHost) {
				chunkReq.HistoryChunks = cloneByteSlices(historyChunks)
			}
			// Schema v3+ omits bodies here (one header-init clone only).
			if streamChunkPayloadIncludesRequestBody(interceptorHost) {
				chunkReq.OriginalRequest = cloneBytes(streamOriginalRequest)
				chunkReq.RequestBody = cloneBytes(streamRequestBody)
			}
			intercepted := interceptStreamChunk(ctx, interceptorHost, chunkReq, execOptions.SkipInterceptorPluginID)
			applyStreamHeaders(intercepted.Headers)
			if len(intercepted.Body) > 0 {
				payload = cloneBytes(intercepted.Body)
			}
			(*chunkIndex)++
			if intercepted.DropChunk {
				return nil, false, nil
			}
		} else {
			(*chunkIndex)++
		}
		if responseSSEValidator != nil {
			validatedPayload, errValidate := responseSSEValidator.AddChunk(payload)
			if errValidate != nil {
				return nil, false, &interfaces.ErrorMessage{StatusCode: http.StatusBadGateway, Error: errValidate}
			}
			payload = validatedPayload
			if len(payload) == 0 {
				return nil, false, nil
			}
		}
		return payload, true, nil
	}

	var bootstrapPayload []byte
	bootstrapChunkIndex := 0
	var bootstrapHistoryChunks [][]byte
	var bootstrapStreamErr error
	var bootstrapErr *interfaces.ErrorMessage
	resetStreamAttempt := func(streamResult *coreexecutor.StreamResult) {
		rawStreamHeaders = cloneHeader(streamResult.Headers)
		baseStreamHeaders = cloneHeader(streamResult.Headers)
		streamHeaderInitialized = false
		streamClosedBeforeRead = false
		streamCanceledBeforeRead = false
		bootstrapStreamErr = nil
		bootstrapErr = nil
		bootstrapPayload = nil
		bootstrapChunkIndex = 0
		bootstrapHistoryChunks = nil
		if responseSSEValidator != nil {
			responseSSEValidator = &sseJSONValidationState{}
		}
		chunks = streamResult.Chunks
		if chunks == nil {
			closed := make(chan coreexecutor.StreamChunk)
			close(closed)
			chunks = closed
		}
	}
	readInitialStreamChunks := func() {
		for {
			chunk, ok, canceled := nextStreamChunk(ctx, nil, nil, chunks)
			if canceled {
				streamCanceledBeforeRead = true
				return
			}
			if !ok {
				streamClosedBeforeRead = true
				applyStreamHeaderInit()
				return
			}
			if chunk.Err != nil {
				bootstrapStreamErr = chunk.Err
				return
			}
			if len(chunk.Payload) == 0 {
				continue
			}
			payload, deliverable, errMsg := transformStreamPayload(chunk.Payload, &bootstrapChunkIndex, bootstrapHistoryChunks)
			if errMsg != nil {
				bootstrapErr = errMsg
				return
			}
			if !deliverable {
				continue
			}
			bootstrapPayload = payload
			return
		}
	}

	bootstrapEligible := func(err error) bool {
		status := statusFromError(err)
		if status == 0 {
			return true
		}
		switch status {
		case http.StatusUnauthorized, http.StatusForbidden, http.StatusPaymentRequired,
			http.StatusRequestTimeout, http.StatusTooManyRequests:
			return true
		default:
			return status >= http.StatusInternalServerError
		}
	}

	maxBootstrapRetries := StreamingBootstrapRetries(h.CurrentConfig())
	if h.AuthManager.HomeEnabled() {
		maxBootstrapRetries = 0
	}
	bootstrapRetries := 0
	var pendingResult *coreexecutor.StreamResult
	// sawStreamResult reports whether any attempt obtained an upstream stream.
	// Failures before that point preserve the legacy contract of returning nil
	// data and header channels, while failures after it are delivered through
	// the stream goroutine (matching bootstrap retry behavior).
	sawStreamResult := false

	for {
		streamResult := pendingResult
		pendingResult = nil
		var errStream error
		if streamResult == nil {
			streamResult, errStream = h.AuthManager.ExecuteStream(attempt.ctx, attempt.providers, attempt.req, attempt.opts)
		}
		if errStream == nil && streamResult == nil {
			errStream = &coreauth.Error{
				Code:       "nil_stream",
				Message:    "auth manager returned nil stream",
				HTTPStatus: http.StatusBadGateway,
			}
		}
		if streamResult != nil {
			sawStreamResult = true
		}

		chunkFailure := false
		if errStream == nil {
			// The after-auth interceptor runs inside ExecuteStream; refresh the
			// request context with its headers before reading the stream.
			if executedReq, executedOpts := attempt.capture.apply(attempt.req, attempt.opts); len(executedOpts.Headers) > 0 || len(executedReq.Payload) > 0 || len(executedOpts.Metadata) > 0 {
				attempt.ctx = enrichContextWithSessionHierarchy(attempt.ctx, executedOpts.Headers, executedReq.Payload, executedOpts.Metadata)
			}
			ctx = attempt.ctx
			resetStreamAttempt(streamResult)
			readInitialStreamChunks()
			if streamCanceledBeforeRead || bootstrapErr != nil || bootstrapStreamErr == nil {
				break
			}
			chunkFailure = true
			errStream = bootstrapStreamErr
		}

		// A bootstrap stream failure may retry on the same model before falling
		// back to another model. Synchronous ExecuteStream errors do not.
		if chunkFailure && bootstrapRetries < maxBootstrapRetries && bootstrapEligible(bootstrapStreamErr) {
			bootstrapRetries++
			retryResult, retryErr := h.AuthManager.ExecuteStream(attempt.ctx, attempt.providers, attempt.req, attempt.opts)
			if retryErr != nil {
				originalBootstrapErr := executionErrorMessage(bootstrapStreamErr)
				if isAuthSelectionUnavailable(retryErr) && originalBootstrapErr.StatusCode >= http.StatusInternalServerError {
					errStream = bootstrapStreamErr
				} else {
					errStream = retryErr
				}
			} else if retryResult == nil {
				errStream = fmt.Errorf("auth manager returned nil stream")
			} else {
				pendingResult = retryResult
				continue
			}
		}

		errStream = enrichAuthSelectionError(errStream, attempt.providers, attempt.normalizedModel)
		fbErr := fallbackEligibleError(errStream)
		clientModel := clientRequestedModel(attemptModel, attemptOpts)
		next, okFallback := h.nextFallbackModel(attempt.ctx, clientModel, fbErr, attemptOpts.FallbackDepth)
		if !okFallback {
			errMsg := executionErrorMessage(errStream)
			if !sawStreamResult {
				lifecycle.completeError(attempt.ctx, errMsg)
				return streamErrorChannels(errMsg)
			}
			bootstrapErr = errMsg
			break
		}
		logModelFallback(clientModel, next, fbErr)

		attemptModel = next
		attemptOpts = h.fallbackExecutionOptions(attemptOpts, clientModel)
		fallbackCtx := contextForModelFallback(baseCtx)
		routeDecision := h.applyModelRouter(fallbackCtx, entryProtocol, attemptModel, rawJSON, true, attemptOpts)
		if routeDecision.ExecutorPluginID != "" {
			bootstrapErr = executionErrorMessage(errStream)
			break
		}
		nextAttempt, buildErr := buildStreamAttempt(fallbackCtx, attemptModel, attemptOpts, routeDecision)
		if buildErr != nil {
			bootstrapErr = buildErr
			break
		}
		attempt = nextAttempt
		activateStreamAttempt(attempt)
		bootstrapRetries = 0
	}

	upstreamHeaders := downstreamHeadersAfterInterceptors(baseStreamHeaders, rawStreamHeaders, passthroughHeadersEnabled)
	if upstreamHeaders == nil && (passthroughHeadersEnabled || streamInterceptorsActive) {
		upstreamHeaders = make(http.Header)
	}
	dataChan := make(chan []byte)
	errChan := make(chan *interfaces.ErrorMessage, 1)
	// Only a response that was actually served by a fallback model is rewritten.
	// Rewriting an ordinary response would leak fallback semantics into requests
	// whose model was remapped by a router or resolved from "auto".
	var rewriteModelName string
	if attemptOpts.FallbackDepth > 0 {
		rewriteModelName = fallbackResponseModel(h.CurrentConfig(), originalRequestedModel, normalizedModel)
	}

	go func() {
		completionOutcome := pluginapi.RequestCompletionSucceeded
		completionStatus := http.StatusOK
		var completionErr error
		defer func() {
			completionOutcome, completionStatus, completionErr = streamDeliveryCompletion(ctx, completionOutcome, completionStatus, completionErr)
			lifecycle.complete(completionOutcome, completionStatus, completionErr)
		}()
		defer close(dataChan)
		defer close(errChan)
		if streamCanceledBeforeRead {
			completionOutcome = pluginapi.RequestCompletionCanceled
			completionStatus = 0
			if ctx != nil {
				completionErr = ctx.Err()
			}
			return
		}

		sendErr := func(msg *interfaces.ErrorMessage) bool {
			if ctx == nil {
				errChan <- msg
				return true
			}
			select {
			case <-ctx.Done():
				return false
			case errChan <- msg:
				return true
			}
		}

		sendData := func(chunk []byte) bool {
			if ctx == nil {
				dataChan <- chunk
				return true
			}
			select {
			case <-ctx.Done():
				return false
			case dataChan <- chunk:
				return true
			}
		}

		var streamRewriter *coreauth.StreamRewriter
		if rewriteModelName != "" {
			streamRewriter = coreauth.NewStreamRewriter(coreauth.StreamRewriteOptions{RewriteModel: rewriteModelName})
		}
		sendPayload := func(chunk []byte) bool {
			if streamRewriter != nil {
				chunk = streamRewriter.RewriteChunk(chunk)
				if len(chunk) == 0 {
					return true
				}
			}
			return sendData(chunk)
		}

		if bootstrapErr != nil {
			completionOutcome = pluginapi.RequestCompletionFailed
			if bootstrapErr.DirectResponse {
				completionOutcome = pluginapi.RequestCompletionRejected
			}
			completionStatus = bootstrapErr.StatusCode
			completionErr = bootstrapErr.Error
			_ = sendErr(bootstrapErr)
			return
		}

		chunkIndex := bootstrapChunkIndex
		historyChunks := bootstrapHistoryChunks
		if bootstrapPayload != nil {
			if okSendData := sendPayload(bootstrapPayload); !okSendData {
				completionOutcome = pluginapi.RequestCompletionCanceled
				completionStatus = 0
				if ctx != nil {
					completionErr = ctx.Err()
				}
				return
			}
			if streamInterceptorsActive && streamChunkPayloadIncludesHistory(interceptorHost) {
				historyChunks = appendStreamInterceptorHistory(historyChunks, bootstrapPayload)
			}
		}
		for {
			chunk, ok, canceled := nextStreamChunk(ctx, nil, &streamClosedBeforeRead, chunks)
			if canceled {
				completionOutcome = pluginapi.RequestCompletionCanceled
				completionStatus = 0
				if ctx != nil {
					completionErr = ctx.Err()
				}
				return
			}
			if !ok {
				if responseSSEValidator != nil {
					if errValidate := responseSSEValidator.Finish(); errValidate != nil {
						errMsg := &interfaces.ErrorMessage{StatusCode: http.StatusBadGateway, Error: errValidate}
						completionOutcome = pluginapi.RequestCompletionFailed
						completionStatus = errMsg.StatusCode
						completionErr = errMsg.Error
						_ = sendErr(errMsg)
						return
					}
				}
				if streamRewriter != nil {
					if tail := streamRewriter.Finish(); len(tail) > 0 && !sendData(tail) {
						completionOutcome = pluginapi.RequestCompletionCanceled
						completionStatus = 0
						if ctx != nil {
							completionErr = ctx.Err()
						}
					}
				}
				return
			}
			if chunk.Err != nil {
				errMsg := executionErrorMessage(chunk.Err)
				completionOutcome = pluginapi.RequestCompletionFailed
				completionStatus = errMsg.StatusCode
				completionErr = chunk.Err
				_ = sendErr(errMsg)
				return
			}
			if len(chunk.Payload) == 0 {
				continue
			}
			payload, deliverable, errMsg := transformStreamPayload(chunk.Payload, &chunkIndex, historyChunks)
			if errMsg != nil {
				completionOutcome = pluginapi.RequestCompletionFailed
				completionStatus = errMsg.StatusCode
				completionErr = errMsg.Error
				_ = sendErr(errMsg)
				return
			}
			if !deliverable {
				continue
			}
			if okSendData := sendPayload(payload); !okSendData {
				completionOutcome = pluginapi.RequestCompletionCanceled
				completionStatus = 0
				if ctx != nil {
					completionErr = ctx.Err()
				}
				return
			}
			if streamInterceptorsActive && streamChunkPayloadIncludesHistory(interceptorHost) {
				historyChunks = appendStreamInterceptorHistory(historyChunks, payload)
			}
		}
	}()
	return dataChan, upstreamHeaders, errChan
}

type sseJSONValidationState struct {
	pending        []byte
	pendingErr     error
	prevEndsWithCR bool
}

func (s *sseJSONValidationState) AddChunk(chunk []byte) ([]byte, error) {
	if s.pendingErr != nil {
		errPending := s.pendingErr
		s.pendingErr = nil
		return nil, errPending
	}
	if len(chunk) == 0 {
		return nil, nil
	}
	if s.prevEndsWithCR {
		if chunk[0] == '\n' {
			chunk = chunk[1:]
		}
		s.prevEndsWithCR = false
	}
	if len(chunk) == 0 {
		return nil, nil
	}
	endsWithCR := chunk[len(chunk)-1] == '\r'
	chunk = bytes.ReplaceAll(chunk, []byte("\r\n"), []byte("\n"))
	chunk = bytes.ReplaceAll(chunk, []byte("\r"), []byte("\n"))
	s.prevEndsWithCR = endsWithCR
	if len(s.pending) > 0 && !bytes.HasSuffix(s.pending, []byte("\n")) && !bytes.HasPrefix(chunk, []byte("\n")) {
		first := bytes.TrimSpace(bytes.SplitN(chunk, []byte("\n"), 2)[0])
		if bytes.HasPrefix(first, []byte("data:")) || bytes.HasPrefix(first, []byte("event:")) {
			s.pending = append(s.pending, '\n')
		}
	}
	s.pending = append(s.pending, chunk...)

	var output []byte
	for {
		frameEnd := bytes.Index(s.pending, []byte("\n\n"))
		if frameEnd < 0 {
			break
		}
		frameEnd += 2
		frame := s.pending[:frameEnd]
		if errValidate := validateSSEFrameDataJSON(frame); errValidate != nil {
			if len(output) > 0 {
				s.pending = s.pending[:0]
				s.pendingErr = errValidate
				return output, nil
			}
			return nil, errValidate
		}
		output = append(output, frame...)
		copy(s.pending, s.pending[frameEnd:])
		s.pending = s.pending[:len(s.pending)-frameEnd]
	}

	if len(bytes.TrimSpace(s.pending)) == 0 {
		s.pending = s.pending[:0]
		return output, nil
	}
	payload, found := sseJSONValidationDataPayload(s.pending)
	payload = bytes.TrimSpace(payload)
	if !found || len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) || json.Valid(payload) {
		output = append(output, s.pending...)
		s.pending = s.pending[:0]
	}
	return output, nil
}

func (s *sseJSONValidationState) Finish() error {
	s.prevEndsWithCR = false
	if s.pendingErr != nil {
		errPending := s.pendingErr
		s.pendingErr = nil
		s.pending = nil
		return errPending
	}
	if len(bytes.TrimSpace(s.pending)) == 0 {
		s.pending = nil
		return nil
	}
	errValidate := validateSSEFrameDataJSON(s.pending)
	s.pending = nil
	return errValidate
}

func sseJSONValidationDataPayload(frame []byte) ([]byte, bool) {
	var payload []byte
	found := false
	for _, line := range bytes.Split(frame, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		if found {
			payload = append(payload, '\n')
		}
		payload = append(payload, bytes.TrimSpace(line[len("data:"):])...)
		found = true
	}
	return payload, found
}

func validateSSEFrameDataJSON(frame []byte) error {
	payload, found := sseJSONValidationDataPayload(frame)
	payload = bytes.TrimSpace(payload)
	if !found || len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) || json.Valid(payload) {
		return nil
	}
	const max = 512
	preview := payload
	if len(preview) > max {
		preview = preview[:max]
	}
	return fmt.Errorf("invalid SSE data JSON (len=%d): %q", len(payload), preview)
}

func validateSSEDataJSON(chunk []byte) error {
	state := &sseJSONValidationState{}
	if _, errAdd := state.AddChunk(chunk); errAdd != nil {
		return errAdd
	}
	return state.Finish()
}

// streamDeliveryCompletion reconciles execution cleanup with the HTTP outcome.
// Call only after closing output channels: the consumer may need EOF to finish.
func streamDeliveryCompletion(ctx context.Context, outcome pluginapi.RequestCompletionOutcome, status int, err error) (pluginapi.RequestCompletionOutcome, int, error) {
	if ctx == nil {
		return outcome, status, err
	}
	deliveryErr, tracked := coreusage.WaitStreamDelivery(ctx)
	if !tracked || (err != nil && !errors.Is(err, context.Canceled)) || outcome == pluginapi.RequestCompletionRejected {
		return outcome, status, err
	}
	if deliveryErr == nil {
		return pluginapi.RequestCompletionSucceeded, http.StatusOK, nil
	}
	if errors.Is(deliveryErr, context.Canceled) || errors.Is(deliveryErr, context.DeadlineExceeded) {
		return pluginapi.RequestCompletionCanceled, 0, deliveryErr
	}
	return pluginapi.RequestCompletionFailed, executionErrorMessage(deliveryErr).StatusCode, deliveryErr
}
