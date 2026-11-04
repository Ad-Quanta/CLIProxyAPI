// Package fallback decides when a failed model attempt may be retried on another model.
package fallback

import (
	"errors"
	"net/http"
	"path"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// Next returns the fallback model at depth for the original client model.
// Depth zero is the first fallback target.
func Next(requested string, depth int, fb config.ModelFallback) (string, bool) {
	if !fb.Enabled || depth < 0 {
		return "", false
	}
	targets := targets(requested, fb)
	if depth >= len(targets) {
		return "", false
	}
	return targets[depth], true
}

// ShouldFallback reports whether err is a capacity or rate-limit failure that
// may switch models. Request-invalid statuses and plugin terminations never qualify.
func ShouldFallback(err error, fb config.ModelFallback) bool {
	if err == nil || !fb.Enabled || len(fb.Rules) == 0 {
		return false
	}
	var terminated *cliproxyexecutor.RequestTerminatedError
	if errors.As(err, &terminated) && terminated != nil {
		return false
	}
	status := statusCode(err)
	if status == 0 {
		return false
	}
	if containsStatus(fb.NoFallbackStatusCodes, status) {
		return false
	}
	return containsStatus(fb.TriggerStatusCodes, status)
}

// PreserveRequestedModel reports whether response model fields should be rewritten
// back to the client-visible model. The default is true.
func PreserveRequestedModel(fb config.ModelFallback) bool {
	if fb.PreserveRequestedModel == nil {
		return true
	}
	return *fb.PreserveRequestedModel
}

func targets(requested string, fb config.ModelFallback) []string {
	base := strings.ToLower(baseModel(requested))
	if base == "" {
		return nil
	}
	for _, rule := range fb.Rules {
		pattern := strings.ToLower(strings.TrimSpace(rule.From))
		if pattern == "" || !modelMatches(pattern, base) {
			continue
		}
		out := make([]string, 0, len(rule.To))
		seen := map[string]struct{}{base: {}}
		for _, target := range rule.To {
			target = strings.TrimSpace(target)
			key := strings.ToLower(baseModel(target))
			if target == "" || key == "" {
				continue
			}
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, target)
		}
		return out
	}
	return nil
}

func modelMatches(pattern string, model string) bool {
	if pattern == model {
		return true
	}
	if !strings.Contains(pattern, "*") {
		return false
	}
	ok, errMatch := path.Match(pattern, model)
	return errMatch == nil && ok
}

func baseModel(model string) string {
	model = strings.TrimSpace(model)
	if strings.HasSuffix(model, ")") {
		if index := strings.LastIndex(model, "("); index > 0 {
			return strings.TrimSpace(model[:index])
		}
	}
	return model
}

func statusCode(err error) int {
	type statusCoder interface {
		StatusCode() int
	}
	var coder statusCoder
	if !errors.As(err, &coder) || coder == nil {
		return 0
	}
	code := coder.StatusCode()
	if code < http.StatusContinue || code > 599 {
		return 0
	}
	return code
}

func containsStatus(codes []int, status int) bool {
	for _, code := range codes {
		if code == status {
			return true
		}
	}
	return false
}
