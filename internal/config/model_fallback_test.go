package config

import (
	"strings"
	"testing"
)

func TestNormalizeModelFallbackDefaultsAndRules(t *testing.T) {
	fb := normalizeModelFallback(ModelFallback{
		Enabled: true,
		Rules: []ModelFallbackRule{
			{From: " GPT-* ", To: []string{"grok-4.7", "grok-4.7", " GPT-* "}},
			{From: "gpt-6-sol", To: []string{"grok-4.7"}},
		},
	})
	if !fb.Enabled {
		t.Fatal("expected fallback to stay enabled")
	}
	if len(fb.TriggerStatusCodes) != 5 || fb.TriggerStatusCodes[0] != 429 || fb.TriggerStatusCodes[2] != 502 {
		t.Fatalf("trigger codes = %v", fb.TriggerStatusCodes)
	}
	if len(fb.NoFallbackStatusCodes) != 4 || fb.NoFallbackStatusCodes[3] != 422 {
		t.Fatalf("no-fallback codes = %v", fb.NoFallbackStatusCodes)
	}
	if len(fb.Rules) != 2 || fb.Rules[0].From != "gpt-*" || len(fb.Rules[0].To) != 1 || fb.Rules[0].To[0] != "grok-4.7" {
		t.Fatalf("rules = %#v", fb.Rules)
	}
	if fb.PreserveRequestedModel != nil {
		t.Fatal("expected omitted preserve flag to stay nil")
	}
}

// TestParseConfigBytesReadsModelFallbackFromHomePayload verifies that the CPA
// node parses model-fallback from the sanitized YAML Home publishes, before a
// Home overlay copies the parsed Config into the runtime config.
func TestParseConfigBytesReadsModelFallbackFromHomePayload(t *testing.T) {
	payload := []byte(`port: 8327
model-fallback:
  enabled: true
  preserve-requested-model: true
  rules:
    - from: "gpt-*"
      to: ["grok-4.7", "grok-4.6"]
`)
	cfg, errParse := ParseConfigBytes(payload)
	if errParse != nil {
		t.Fatalf("ParseConfigBytes() error = %v", errParse)
	}
	if cfg == nil || !cfg.ModelFallback.Enabled {
		t.Fatalf("model-fallback not enabled: %#v", cfg)
	}
	if cfg.ModelFallback.PreserveRequestedModel == nil || !*cfg.ModelFallback.PreserveRequestedModel {
		t.Fatalf("preserve-requested-model = %#v, want true", cfg.ModelFallback.PreserveRequestedModel)
	}
	if len(cfg.ModelFallback.Rules) != 1 {
		t.Fatalf("rules = %#v", cfg.ModelFallback.Rules)
	}
	rule := cfg.ModelFallback.Rules[0]
	if !strings.EqualFold(rule.From, "gpt-*") || len(rule.To) != 2 || rule.To[0] != "grok-4.7" || rule.To[1] != "grok-4.6" {
		t.Fatalf("rule = %#v", rule)
	}
	if len(cfg.ModelFallback.TriggerStatusCodes) != 5 {
		t.Fatalf("trigger codes = %v", cfg.ModelFallback.TriggerStatusCodes)
	}
}
