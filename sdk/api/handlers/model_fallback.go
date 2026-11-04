package handlers

import (
	"errors"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/clienterror"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/fallback"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
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
	if h == nil || h.AuthManager == nil || (ctx != nil && ctx.Err() != nil) {
		return "", false
	}
	cfg := h.CurrentConfig()
	if cfg == nil {
		return "", false
	}
	fb := cfg.ModelFallback
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

// fallbackStatusError adds a StatusCode to errors that expose one only through
// the shared client-error extraction, such as context deadline failures.
type fallbackStatusError struct {
	err    error
	status int
}

func (e *fallbackStatusError) Error() string {
	if e == nil || e.err == nil {
		return ""
	}
	return e.err.Error()
}

func (e *fallbackStatusError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

func (e *fallbackStatusError) StatusCode() int {
	if e == nil {
		return 0
	}
	return e.status
}

// fallbackEligibleError normalizes an execution error so fallback decisions can
// use the same status extraction as the HTTP response path.
func fallbackEligibleError(err error) error {
	if err == nil || fallbackStatus(err) != 0 {
		return err
	}
	if status := clienterror.HTTPStatusFromError(err); status > 0 {
		return &fallbackStatusError{err: err, status: status}
	}
	return err
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
