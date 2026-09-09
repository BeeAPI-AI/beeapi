package reasoning

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"slices"
	"testing"
)

func TestModelSpecificNativeLevels(t *testing.T) {
	for _, tt := range []struct {
		model, protocol string
		levels          []string
	}{
		{"gpt-6-astra", Responses, []string{"low", "medium", "high", "xhigh", "max"}},
		{"gpt-5.6-sol", Responses, []string{"none", "low", "medium", "high", "xhigh", "max"}},
		{"gpt-5.6-terra", Chat, []string{"none", "low", "medium", "high", "xhigh", "max"}},
		{"gpt-5.6-luna", Chat, []string{"none", "low", "medium", "high", "xhigh", "max"}},
		{"openai/gpt-5.5", Responses, []string{"none", "low", "medium", "high", "xhigh"}},
		{"claude-opus-4-6", Messages, []string{"low", "medium", "high", "max"}},
		{"claude-sonnet-4-6", Messages, []string{"low", "medium", "high", "max"}},
		{"claude-opus-4-7", Messages, []string{"low", "medium", "high", "xhigh", "max"}},
		{"claude-opus-5", Messages, []string{"low", "medium", "high", "xhigh", "max"}},
		{"deepseek-v4-pro", Responses, []string{"low", "high", "max"}},
		{"deepseek-v4-flash", Chat, []string{"low", "high", "max"}},
		{"deepseek-v4-pro", Messages, []string{"low", "high", "max"}},
		{"glm-5.2", Chat, []string{"high", "max"}},
		{"kimi-k3", Chat, []string{"low", "high", "max"}},
		{"grok-4.6", Responses, []string{"low", "medium", "high", "xhigh"}},
		{"grok-4.5", Chat, []string{"low", "medium", "high"}},
		{"qwen3.8-max", Chat, []string{"low", "medium", "xhigh"}},
		{"gemini-3-pro-preview", Contents, []string{"low", "high"}},
		{"gemini-3.1-pro-preview", Contents, []string{"low", "medium", "high"}},
		{"gemini-3.8-flash", Contents, []string{"low", "medium", "high"}},
	} {
		t.Run(tt.model+"/"+tt.protocol, func(t *testing.T) {
			got := Normalize(tt.protocol, Fallback(tt.model, tt.protocol))
			if !reflect.DeepEqual(got.Efforts, tt.levels) {
				t.Fatalf("levels=%v want %v", got.Efforts, tt.levels)
			}
		})
	}
}

func TestDoesNotInventSupportFromNamesOrTranslations(t *testing.T) {
	for _, model := range []string{"2K-Max", "gpt-5.6-secret", "gpt-5.7", "gpt-5.6-pro", "grok-4.99", "claude-opus-7", "MiniMax-M3", "MiniMax-M2.7", "glm-5", "kimi-k2.5", "qwen3.5-plus", "anthropic/gpt-5.6", "gpt-5.6-sol-99999999", "merchant/gpt-5.6"} {
		if got := Fallback(model, Chat); len(got.Efforts) != 0 {
			t.Errorf("%q invented levels: %v", model, got)
		}
	}
	for _, tt := range []struct{ model, protocol string }{
		{"gpt-5.6-sol", Messages}, {"claude-opus-5", Chat}, {"gemini-3.1-pro-preview", Responses}, {"glm-5.2", Messages}, {"kimi-k3", Responses},
		{"gpt-5.3-codex", Chat},
	} {
		if got := Fallback(tt.model, tt.protocol); len(got.Efforts) != 0 {
			t.Errorf("invented translation: %v %v", tt, got)
		}
	}
	if got := Fallback("gpt-5.6-sol-2026-09-01", Responses); !slices.Contains(got.Efforts, "max") {
		t.Fatal("lost known dated snapshot")
	}
}

func TestRouteCapabilityOverridesCatalogueWithoutWidening(t *testing.T) {
	snapshot := Selection{Model: "gpt-5.6-sol", Protocol: Responses, Capability: Capability{Mode: "effort", Efforts: []string{"high", "low"}, Default: "low"}}
	got := Resolve("codex", snapshot.Model, &snapshot)
	if !reflect.DeepEqual(got.Efforts, []string{"low", "high"}) || got.Default != "low" {
		t.Fatalf("route did not win: %#v", got)
	}
	for _, mode := range []string{"none", "", "future_mode"} {
		snapshot.Capability.Mode = mode
		if got := Resolve("codex", snapshot.Model, &snapshot); len(got.Efforts) != 0 {
			t.Fatalf("invalid/disabled metadata fell back: %#v", got)
		}
	}
	snapshot.Capability = Capability{Mode: "effort", Efforts: []string{"max"}}
	if got := Resolve("codex", "gpt-5.5", &snapshot); len(got.Efforts) != 0 {
		t.Fatal("stale model binding accepted")
	}
	if got := Resolve("hermes", snapshot.Model, &snapshot); len(got.Efforts) != 0 {
		t.Fatal("stale protocol binding accepted")
	}
}

func TestInvalidDeclarationsFailClosed(t *testing.T) {
	for _, capability := range []Capability{
		{Mode: "effort"}, {Mode: "effort", Efforts: []string{"max", "max"}},
		{Mode: "effort", Efforts: []string{"MAX"}}, {Mode: "effort", Efforts: []string{"ultra"}},
		{Mode: "effort", Efforts: []string{"high"}, Default: "max"},
		{Mode: "budget", Efforts: []string{"high"}, Budgets: map[string]int{"high": 999999}},
		{Mode: "budget", Efforts: []string{"high"}},
	} {
		protocol := Chat
		if capability.Mode == "budget" {
			protocol = Contents
		}
		if got := Normalize(protocol, capability); len(got.Efforts) != 0 {
			t.Fatalf("accepted invalid capability: %#v", got)
		}
	}
	if got := Normalize(Contents, Capability{Mode: "effort", Efforts: []string{"max"}}); len(got.Efforts) != 0 {
		t.Fatal("accepted effort field on Gemini")
	}
}

func TestNativeIntersectionsAndBudgetCopy(t *testing.T) {
	c := Fallback("deepseek-v4-pro", Responses)
	if got := Values("grok", c); !reflect.DeepEqual(got, []string{"low", "high"}) {
		t.Fatalf("invented Grok max/xhigh support: %v", got)
	}
	if got := Values("openclaw", c); !reflect.DeepEqual(got, []string{"low", "high", "max"}) {
		t.Fatalf("lost native max: %v", got)
	}
	if Default(c, Values("grok", c)) != "high" {
		t.Fatal("invalid tool default")
	}
	if got := Resolve("claude-desktop", "claude-opus-5", nil); len(got.Efforts) != 0 {
		t.Fatal("invented Desktop field")
	}
	source := map[string]Selection{"gemini": {Model: "gemini-2.5-pro", Protocol: Contents, Capability: Fallback("gemini-2.5-pro", Contents)}}
	copy := Clone(source)
	copy["gemini"].Capability.Budgets["high"] = 1
	copy["gemini"].Capability.Efforts[0] = "max"
	if source["gemini"].Capability.Budgets["high"] != 32768 || source["gemini"].Capability.Efforts[0] != "minimal" {
		t.Fatal("snapshot clone aliases memory")
	}
}

func TestCodexSchemaProof(t *testing.T) {
	for _, tt := range []struct {
		raw  string
		want bool
	}{
		{`{"definitions":{"ReasoningEffort":{"type":"string","enum":["low","high","xhigh"]}}}`, false},
		{`{"definitions":{"ReasoningEffort":{"type":"string","enum":["low","high","max"]}}}`, true},
		{`{"definitions":{"v2":{"ReasoningEffort":{"type":"string","minLength":1}}}}`, true},
		{`{"definitions":{"RandomField":{"type":"string","minLength":1}}}`, false},
		{`{"definitions":{"ReasoningEffort":{"type":"string","minLength":1,"pattern":"low|high"}}}`, false},
	} {
		var schema any
		if err := json.Unmarshal([]byte(tt.raw), &schema); err != nil {
			t.Fatal(err)
		}
		if got := schemaAllowsMax(schema); got != tt.want {
			t.Fatalf("schema=%s got=%v", tt.raw, got)
		}
	}
}

func TestInstalledCodexMaxProbe(t *testing.T) {
	if os.Getenv("GETBEE_TEST_CODEX_RUNTIME") != "1" {
		t.Skip("opt-in, installed Codex schema generator only; no model requests")
	}
	if !CodexSupportsMax(context.Background()) {
		t.Fatal("installed Codex did not expose max-capable ReasoningEffort")
	}
}
