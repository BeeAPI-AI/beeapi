package configurator

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/BeeAPI-AI/beeapi/internal/reasoning"
	"github.com/BeeAPI-AI/beeapi/internal/state"
)

func TestClaudeMaxUsesEnvironmentAndCanBeSwitchedAndRolledBack(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GETBEE_TARGET_HOME", home)
	path := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	original := []byte(`{"effortLevel":"high","alwaysThinkingEnabled":false,"env":{"CLAUDE_CODE_EFFORT_LEVEL":"low","CLAUDE_CODE_DISABLE_ADAPTIVE_THINKING":"1","KEEP_ME":"yes"},"permissions":{"allow":["Read"]},"modelSettings":{"claude-opus-4-6":{"effortLevel":"low"}}}`)
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	store := &state.Store{Dir: t.TempDir()}
	options := Options{Endpoint: "https://beeapi.ai", APIKey: "sk-test", Agents: []string{"claude"}, Model: "claude-opus-4-6", ReasoningEfforts: map[string]string{"claude": "max"}}
	result, err := Apply(store, options)
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	env := cfg["env"].(map[string]any)
	if cfg["effortLevel"] != nil || env["CLAUDE_CODE_EFFORT_LEVEL"] != "max" || env["CLAUDE_CODE_DISABLE_ADAPTIVE_THINKING"] != nil || cfg["alwaysThinkingEnabled"] != true {
		t.Fatalf("invalid max activation: %s", raw)
	}
	if env["KEEP_ME"] != "yes" || cfg["permissions"] == nil || cfg["modelSettings"] == nil {
		t.Fatal("changed unrelated user settings")
	}
	options.ReasoningEfforts["claude"] = "low"
	if _, err := Apply(store, options); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(path)
	if strings.Contains(string(raw), `"max"`) || !strings.Contains(string(raw), `"effortLevel": "low"`) {
		t.Fatalf("max was not cleared: %s", raw)
	}
	options.ReasoningEfforts = nil
	if _, err := Apply(store, options); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(path)
	var reset map[string]any
	if err := json.Unmarshal(raw, &reset); err != nil {
		t.Fatal(err)
	}
	if reset["env"].(map[string]any)["CLAUDE_CODE_EFFORT_LEVEL"] != nil || reset["effortLevel"] != nil {
		t.Fatalf("reasoning override retained: %s", raw)
	}
	if _, err := store.Rollback(result.BackupID); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(path)
	if string(raw) != string(original) {
		t.Fatalf("rollback did not restore original bytes: %s", raw)
	}
}

func TestMaxForNativeOpenAICompatibleAdapters(t *testing.T) {
	previous := probeCodexMax
	probeCodexMax = func() bool { return true }
	t.Cleanup(func() { probeCodexMax = previous })
	home := t.TempDir()
	t.Setenv("GETBEE_TARGET_HOME", home)
	store := &state.Store{Dir: t.TempDir()}
	agents := []string{"codex", "opencode", "openclaw", "hermes"}
	models := map[string]string{"codex": "gpt-5.6-sol", "opencode": "deepseek-v4-pro", "openclaw": "deepseek-v4-pro", "hermes": "kimi-k3"}
	efforts := map[string]string{"codex": "max", "opencode": "max", "openclaw": "max", "hermes": "max"}
	if _, err := Apply(store, Options{Endpoint: "https://beeapi.dev", APIKey: "sk-test", Agents: agents, Models: models, ReasoningEfforts: efforts}); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{
		filepath.Join(home, ".codex", "config.toml"):                `model_reasoning_effort = "max"`,
		filepath.Join(home, ".config", "opencode", "opencode.json"): `"reasoningEffort": "max"`,
		filepath.Join(home, ".openclaw", "openclaw.json"):           `"thinkingDefault": "max"`,
		filepath.Join(home, ".hermes", "config.yaml"):               `reasoning_effort: "max"`,
	} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(raw), want) {
			t.Fatalf("%s missing %s: %s", path, want, raw)
		}
	}
	raw, _ := os.ReadFile(filepath.Join(home, ".openclaw", "openclaw.json"))
	if strings.Contains(string(raw), `"xhigh"`) || strings.Contains(string(raw), `"medium"`) {
		t.Fatalf("invented DeepSeek aliases: %s", raw)
	}
}

func TestRejectsInvalidEffortsBeforeAnyNativeWrites(t *testing.T) {
	previous := probeCodexMax
	probeCodexMax = func() bool { return false }
	t.Cleanup(func() { probeCodexMax = previous })
	for _, tt := range []struct{ agent, model, effort string }{
		{"codex", "gpt-5.6-sol", "max"}, {"opencode", "gpt-5.5", "max"},
		{"claude", "claude-opus-4-6", "xhigh"}, {"grok", "grok-4.5", "xhigh"},
		{"grok", "deepseek-v4-pro", "max"}, {"hermes", "deepseek-v4-pro", "xhigh"},
		{"gemini", "gemini-3-pro-preview", "medium"}, {"opencode", "MiniMax-M3", "high"},
	} {
		t.Run(tt.agent+tt.model, func(t *testing.T) {
			root := t.TempDir()
			home := filepath.Join(root, "home")
			t.Setenv("GETBEE_TARGET_HOME", home)
			_, err := Apply(&state.Store{Dir: filepath.Join(root, "state")}, Options{Endpoint: "https://beeapi.dev", APIKey: "sk-test", Agents: []string{tt.agent}, Model: tt.model, ReasoningEfforts: map[string]string{tt.agent: tt.effort}})
			if err == nil {
				t.Fatal("accepted invalid effort")
			}
			if _, err := os.Stat(home); !os.IsNotExist(err) {
				t.Fatal("invalid configuration touched native files")
			}
		})
	}
}

func TestRouteAliasPersistsAndDoesNotWidenNativeLists(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GETBEE_TARGET_HOME", home)
	snapshot := reasoning.Selection{Model: "custom-responses-model", Protocol: reasoning.Responses, Capability: reasoning.Capability{Mode: "effort", Efforts: []string{"low", "high"}, Default: "high"}}
	options := Options{Endpoint: "https://beeapi.dev", APIKey: "sk-test", Agents: []string{"grok"}, Model: snapshot.Model, ReasoningEfforts: map[string]string{"grok": "high"}, ReasoningSelections: map[string]reasoning.Selection{"grok": snapshot}}
	if _, err := Apply(&state.Store{Dir: t.TempDir()}, options); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(home, ".grok", "config.toml"))
	if !strings.Contains(string(raw), `reasoning_efforts = ["low", "high"]`) {
		t.Fatalf("capability list widened: %s", raw)
	}
	options.ReasoningEfforts["grok"] = "xhigh"
	if _, err := Apply(&state.Store{Dir: t.TempDir()}, options); err == nil {
		t.Fatal("ignored restrictive route")
	}
}

func TestGeminiRouteBudgetIsNativeNotEffort(t *testing.T) {
	cap := reasoning.Capability{Mode: "budget", Efforts: []string{"low", "high"}, Default: "low", Budgets: map[string]int{"low": 128, "high": 2048}}
	if got := thinkingConfig(cap, "high"); !reflect.DeepEqual(got, map[string]any{"thinkingBudget": 2048}) {
		t.Fatalf("wrong budget payload: %v", got)
	}
}
