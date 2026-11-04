package handlers

import (
	"errors"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/fallback"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	log "github.com/sirupsen/logrus"
	"golang.org/x/net/context"
)

func clientRequestedModel(modelName string, opts modelExecutionOptions) string {
	if clientModel := strings.TrimSpace(opts.ClientModel); clientModel != "" {
		return clientModel
	}
	return modelName
}

func (h *BaseAPIHandler) nextFallbackModel(ctx context.Context, requested string, err error, depth int) (string, bool) {
	if h == nil || h.Cfg == nil || h.AuthManager == nil || (ctx != nil && ctx.Err() != nil) {
		return "", false
	}
	fb := h.Cfg.ModelFallback
	if !fallback.ShouldFallback(err, fb) {
		return "", false
	}
	next, ok := fallback.Next(requested, depth, fb)
	if !ok || strings.EqualFold(strings.TrimSpace(next), strings.TrimSpace(requested)) {
		return "", false
	}
	if !h.AuthManager.HomeEnabled() && len(util.GetProviderName(next)) == 0 {
		return "", false
	}
	return next, true
}

func (h *BaseAPIHandler) fallbackExecutionOptions(opts modelExecutionOptions, clientModel string) modelExecutionOptions {
	opts.FallbackDepth++
	opts.ClientModel = clientModel
	opts.ForcedProvider = ""
	opts.AuthSelectionModel = ""
	return opts
}

func contextForModelFallback(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx = context.WithValue(ctx, preparedModelRouteContextKey{}, nil)
	ctx = context.WithValue(ctx, pinnedAuthContextKey{}, nil)
	return ctx
}

func logModelFallback(clientModel string, nextModel string, err error) {
	log.WithFields(log.Fields{
		"requested_model": clientModel,
		"fallback_model":  nextModel,
		"status_code":     fallbackStatus(err),
	}).Info("model fallback triggered")
}

func fallbackStatus(err error) int {
	type statusCoder interface {
		StatusCode() int
	}
	var coder statusCoder
	if errors.As(err, &coder) && coder != nil {
		return coder.StatusCode()
	}
	return 0
}

func fallbackResponseModel(cfg *config.SDKConfig, clientModel string, attemptModel string) string {
	clientModel = strings.TrimSpace(clientModel)
	attemptModel = strings.TrimSpace(attemptModel)
	if cfg == nil || clientModel == "" || strings.EqualFold(clientModel, attemptModel) {
		return ""
	}
	if !fallback.PreserveRequestedModel(cfg.ModelFallback) {
		return ""
	}
	return clientModel
}

func rewriteFallbackResponse(cfg *config.SDKConfig, clientModel string, attemptModel string, body []byte) []byte {
	model := fallbackResponseModel(cfg, clientModel, attemptModel)
	if model == "" || len(body) == 0 {
		return body
	}
	rewriter := coreauth.NewStreamRewriter(coreauth.StreamRewriteOptions{RewriteModel: model})
	rewritten := rewriter.RewriteChunk(body)
	if len(rewritten) == 0 {
		if tail := rewriter.Finish(); len(tail) > 0 {
			return tail
		}
		return body
	}
	return rewritten
}

func rewriteModelStream(in <-chan coreexecutor.StreamChunk, model string) <-chan coreexecutor.StreamChunk {
	out := make(chan coreexecutor.StreamChunk)
	go func() {
		defer close(out)
		rewriter := coreauth.NewStreamRewriter(coreauth.StreamRewriteOptions{RewriteModel: model})
		for chunk := range in {
			if chunk.Err != nil || len(chunk.Payload) == 0 {
				out <- chunk
				continue
			}
			payload := rewriter.RewriteChunk(chunk.Payload)
			if len(payload) == 0 {
				continue
			}
			chunk.Payload = payload
			out <- chunk
		}
		if tail := rewriter.Finish(); len(tail) > 0 {
			out <- coreexecutor.StreamChunk{Payload: tail}
		}
	}()
	return out
}
