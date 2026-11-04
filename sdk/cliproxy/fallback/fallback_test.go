package fallback

import (
	"errors"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

type statusError struct {
	status int
}

func (e statusError) Error() string { return http.StatusText(e.status) }

func (e statusError) StatusCode() int { return e.status }

func capacityFallback() config.ModelFallback {
	cfg := &config.Config{SDKConfig: config.SDKConfig{ModelFallback: config.ModelFallback{
		Enabled: true,
		Rules: []config.ModelFallbackRule{
			{From: "gpt-*", To: []string{"grok-4.7", "grok-4"}},
			{From: "gpt-6-sol", To: []string{"grok-other"}},
		},
	}}}
	cfg.SanitizeModelFallback()
	return cfg.ModelFallback
}

func TestNextMatchesWildcardBeforeLaterExactRule(t *testing.T) {
	fb := capacityFallback()

	got, ok := Next("gpt-6-sol", 0, fb)
	if !ok || got != "grok-4.7" {
		t.Fatalf("Next(gpt-6-sol) = (%q, %t)", got, ok)
	}
	got, ok = Next("gpt-6.1-terra(high)", 1, fb)
	if !ok || got != "grok-4" {
		t.Fatalf("Next(second) = (%q, %t)", got, ok)
	}
	if _, ok = Next("claude-sonnet", 0, fb); ok {
		t.Fatal("expected unrelated model to have no fallback")
	}
}

func TestShouldFallbackOnlyCapacityStatuses(t *testing.T) {
	fb := capacityFallback()

	if !ShouldFallback(statusError{status: http.StatusBadGateway}, fb) {
		t.Fatal("expected 502 to qualify")
	}
	if !ShouldFallback(statusError{status: http.StatusTooManyRequests}, fb) {
		t.Fatal("expected 429 to qualify")
	}
	if ShouldFallback(statusError{status: http.StatusUnprocessableEntity}, fb) {
		t.Fatal("did not expect 422 to qualify")
	}
	if ShouldFallback(statusError{status: http.StatusBadRequest}, fb) {
		t.Fatal("did not expect 400 to qualify")
	}
	if ShouldFallback(errors.New("server_is_overloaded"), fb) {
		t.Fatal("did not expect an error without a status to qualify")
	}
	if !PreserveRequestedModel(fb) {
		t.Fatal("expected preserve to default true")
	}
}
