package config

import "testing"

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
