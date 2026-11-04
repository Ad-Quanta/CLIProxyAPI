package config

import (
	"net/http"
	"strings"
)

// ModelFallback configures a cross-model retry that runs only after the
// requested model fails and before any response byte is written.
type ModelFallback struct {
	// Enabled toggles the fallback chain. Default is false.
	Enabled bool `yaml:"enabled" json:"enabled"`

	// PreserveRequestedModel rewrites response model fields back to the client
	// requested model. Nil inherits the default, which is true.
	PreserveRequestedModel *bool `yaml:"preserve-requested-model,omitempty" json:"preserve-requested-model,omitempty"`

	// TriggerStatusCodes are upstream statuses that may switch models.
	// Empty means 429, 500, 502, 503, and 504.
	TriggerStatusCodes []int `yaml:"trigger-status-codes,omitempty" json:"trigger-status-codes,omitempty"`

	// NoFallbackStatusCodes never switch models. Empty means 400, 401, 403, and 422.
	NoFallbackStatusCodes []int `yaml:"no-fallback-status-codes,omitempty" json:"no-fallback-status-codes,omitempty"`

	// Rules maps a requested model, or a trailing-wildcard pattern, to ordered targets.
	Rules []ModelFallbackRule `yaml:"rules,omitempty" json:"rules,omitempty"`
}

// ModelFallbackRule is one requested-model to fallback-model mapping.
type ModelFallbackRule struct {
	From string   `yaml:"from" json:"from"`
	To   []string `yaml:"to" json:"to"`
}

// SanitizeModelFallback normalizes fallback rules and fills default status sets.
func (cfg *Config) SanitizeModelFallback() {
	if cfg == nil {
		return
	}
	cfg.ModelFallback = normalizeModelFallback(cfg.ModelFallback)
}

func normalizeModelFallback(fb ModelFallback) ModelFallback {
	fb.TriggerStatusCodes = normalizeStatusCodes(fb.TriggerStatusCodes, []int{
		http.StatusTooManyRequests,
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout,
	})
	fb.NoFallbackStatusCodes = normalizeStatusCodes(fb.NoFallbackStatusCodes, []int{
		http.StatusBadRequest,
		http.StatusUnauthorized,
		http.StatusForbidden,
		http.StatusUnprocessableEntity,
	})
	if len(fb.Rules) == 0 {
		fb.Rules = nil
		return fb
	}
	rules := make([]ModelFallbackRule, 0, len(fb.Rules))
	for _, rule := range fb.Rules {
		from := strings.ToLower(strings.TrimSpace(rule.From))
		if from == "" || len(rules) >= 32 {
			continue
		}
		seen := make(map[string]struct{}, len(rule.To))
		targets := make([]string, 0, len(rule.To))
		for _, target := range rule.To {
			target = strings.TrimSpace(target)
			if target == "" || len(targets) >= 4 {
				continue
			}
			key := strings.ToLower(target)
			if key == from {
				continue
			}
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			targets = append(targets, target)
		}
		if len(targets) == 0 {
			continue
		}
		rules = append(rules, ModelFallbackRule{From: from, To: targets})
	}
	fb.Rules = rules
	return fb
}

func normalizeStatusCodes(codes []int, defaults []int) []int {
	if len(codes) == 0 {
		return append([]int(nil), defaults...)
	}
	seen := make(map[int]struct{}, len(codes))
	out := make([]int, 0, len(codes))
	for _, code := range codes {
		if code < 100 || code > 599 {
			continue
		}
		if _, ok := seen[code]; ok {
			continue
		}
		seen[code] = struct{}{}
		out = append(out, code)
	}
	if len(out) == 0 {
		return append([]int(nil), defaults...)
	}
	return out
}
