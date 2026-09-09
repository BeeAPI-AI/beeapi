package app

import (
	"bufio"
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/BeeAPI-AI/beeapi/internal/beeapi"
	"github.com/BeeAPI-AI/beeapi/internal/reasoning"
	"github.com/BeeAPI-AI/beeapi/internal/state"
)

func TestReasoningMenuUsesRouteAndRejectsCompatibilityAliases(t *testing.T) {
	for _, tt := range []struct {
		name, agent, model, input, want string
		metadata                        map[string]reasoning.Capability
		codexMax                        bool
		must, forbidden                 string
	}{
		{name: "DeepSeek max is not xhigh", agent: "opencode", model: "deepseek-v4-pro", input: "xhigh\nmax\n", want: "max", must: "思考等级无效", forbidden: "xhigh ·"},
		{name: "route shrinks canonical model", agent: "opencode", model: "gpt-5.6-sol", input: "max\nhigh\n", want: "high", metadata: map[string]reasoning.Capability{reasoning.Chat: {Mode: "effort", Efforts: []string{"high"}, Default: "high"}}, must: "思考等级无效", forbidden: "max ·"},
		{name: "merchant alias needs explicit route", agent: "hermes", model: "merchant-coding", input: "max\n", want: "max", metadata: map[string]reasoning.Capability{reasoning.Chat: {Mode: "effort", Efforts: []string{"high", "max"}, Default: "high"}}, must: "最高推理"},
		{name: "do not guess alias", agent: "opencode", model: "my-max-key", input: "", must: "未确认可设置", forbidden: "请选择思考等级"},
		{name: "no automatic translation", agent: "claude", model: "gpt-5.6-sol", input: "", must: "未确认可设置", forbidden: "请选择思考等级"},
		{name: "explicit translation", agent: "claude", model: "gpt-5.6-sol", input: "max\n", want: "max", metadata: map[string]reasoning.Capability{reasoning.Messages: {Mode: "effort", Efforts: []string{"high", "max"}}}, must: "最高推理"},
		{name: "explicit disable wins", agent: "codex", model: "gpt-5.6-sol", metadata: map[string]reasoning.Capability{reasoning.Responses: {Mode: "none"}}, must: "未确认可设置", forbidden: "请选择思考等级"},
		{name: "missing protocol is not fallback", agent: "codex", model: "gpt-5.6-sol", metadata: map[string]reasoning.Capability{}, must: "未确认可设置", forbidden: "请选择思考等级"},
		{name: "Codex old runtime", agent: "codex", model: "gpt-5.6-sol", input: "xhigh\n", want: "xhigh", must: "本机 Codex 尚未确认支持 max", forbidden: "max ·"},
		{name: "Codex confirmed max", agent: "codex", model: "gpt-5.6-sol", input: "max\n", want: "max", codexMax: true, must: "最高推理"},
		{name: "Gemini token budget", agent: "gemini", model: "gemini-2.5-pro", input: "high\n", want: "high", must: "32768 tokens", forbidden: "max ·"},
		{name: "Gemini model specific", agent: "gemini", model: "gemini-3-pro-preview", input: "medium\nhigh\n", want: "high", must: "思考等级无效", forbidden: "medium ·"},
		{name: "default removes override", agent: "hermes", model: "kimi-k3", input: "0\n", must: "不设置推理档位"},
		{name: "none differs from no override", agent: "opencode", model: "gpt-5.6-sol", input: "none\n", want: "none", must: "思考等级 none"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			input := strings.NewReader(tt.input)
			var output bytes.Buffer
			r := &runner{in: input, reader: bufio.NewReader(input), out: &output, errOut: &output, codexMaxSupport: func() bool { return tt.codexMax }}
			credential := credentialMaterial{ID: "key", ModelOptionsAuthoritative: true, ModelOptions: []beeapi.ModelOption{{ID: tt.model, Capabilities: []string{"reasoning"}, Reasoning: tt.metadata}}}
			got, err := r.selectReasoningEfforts([]string{tt.agent}, []credentialMaterial{credential}, map[string]string{tt.agent: "key"}, map[string]string{tt.agent: tt.model}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if got[tt.agent] != tt.want {
				t.Fatalf("got=%v want=%q output=%s", got, tt.want, &output)
			}
			if !strings.Contains(output.String(), tt.must) || (tt.forbidden != "" && strings.Contains(output.String(), tt.forbidden)) {
				t.Fatalf("unexpected menu:\n%s", &output)
			}
		})
	}
}

func TestMissingReasoningFlagDoesNotInventControls(t *testing.T) {
	c := credentialMaterial{ModelOptionsAuthoritative: true, ModelOptions: []beeapi.ModelOption{{ID: "gpt-5.6-sol"}}}
	if got := selectionForModel("codex", "gpt-5.6-sol", c); len(got.Capability.Efforts) != 0 {
		t.Fatalf("ignored authoritative flag: %+v", got)
	}
	if got := selectionForModel("codex", "gpt-6-astra", c); len(got.Capability.Efforts) != 0 {
		t.Fatalf("invented missing model: %+v", got)
	}
}

func TestReasoningSnapshotSurvivesSaveProjectionAndMixedPlans(t *testing.T) {
	cap := reasoning.Capability{Mode: "effort", Efforts: []string{"low", "high", "max"}, Default: "high"}
	snapshot := reasoning.Selection{Model: "merchant-model", Protocol: reasoning.Chat, Capability: cap}
	profile := state.Profile{ID: "work", Name: "Work", Endpoint: "https://beeapi.ai", Agents: []string{"hermes"}, Models: map[string]string{"hermes": "merchant-model"}, AgentCredentials: map[string]string{"hermes": "work-key"}, ReasoningEfforts: map[string]string{"hermes": "max"}, ReasoningSelections: map[string]reasoning.Selection{"hermes": snapshot}}
	cfg := state.Config{Endpoint: profile.Endpoint, Agents: []string{"grok"}, Models: map[string]string{"grok": "grok-4.6"}, ReasoningEfforts: map[string]string{"grok": "xhigh"}, Profiles: []state.Profile{profile}}
	activateProfileFields(&cfg, profile)
	if cfg.ReasoningEfforts["grok"] != "xhigh" {
		t.Fatal("changed another tool")
	}
	store := &state.Store{Dir: t.TempDir()}
	if err := store.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	saved, err := store.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	projected := profileProjection(saved.Profiles[0], "hermes")
	if !reflect.DeepEqual(projected.ReasoningSelections["hermes"], snapshot) {
		t.Fatalf("lost route snapshot: %+v", projected)
	}
	current := profileFromCurrent(saved, "current", "Current", saved.UpdatedAt)
	current.ReasoningSelections["hermes"].Capability.Efforts[0] = "none"
	if saved.ReasoningSelections["hermes"].Capability.Efforts[0] != "low" {
		t.Fatal("clone aliases source")
	}
	profile.ReasoningSelections = nil
	profile.ReasoningEfforts = nil
	activateProfileFields(&saved, profile)
	if _, ok := saved.ReasoningSelections["hermes"]; ok {
		t.Fatal("stale metadata retained")
	}
}

func TestModelChangeDoesNotSilentlyTranslatePreviousMax(t *testing.T) {
	var output bytes.Buffer
	r := &runner{out: &output, errOut: &output}
	models := map[string]string{"opencode": "gpt-5.5"}
	snapshot := reasoning.Selection{Model: models["opencode"], Protocol: reasoning.Chat, Capability: reasoning.Fallback(models["opencode"], reasoning.Chat)}
	got := r.retainedReasoningEfforts([]string{"opencode"}, models, map[string]string{"opencode": "max"}, map[string]reasoning.Selection{"opencode": snapshot})
	if len(got) != 0 || !strings.Contains(output.String(), "已清除") {
		t.Fatalf("silently remapped max: %v %s", got, &output)
	}
}

func TestChangedRouteMetadataRequiresReapply(t *testing.T) {
	profile := state.Profile{ID: "plan", Endpoint: "https://beeapi.ai", Agents: []string{"opencode"}, Models: map[string]string{"opencode": "gpt-5.6-sol"}, AgentCredentials: map[string]string{"opencode": "key"}, ReasoningEfforts: map[string]string{"opencode": "high"}, ReasoningSelections: map[string]reasoning.Selection{"opencode": {Model: "gpt-5.6-sol", Protocol: reasoning.Chat, Capability: reasoning.Fallback("gpt-5.6-sol", reasoning.Chat)}}}
	cfg := state.Config{}
	activateProfileFields(&cfg, profile)
	if !profileAlreadyApplied(cfg, profile) {
		t.Fatal("equal profile not recognized")
	}
	selection := profile.ReasoningSelections["opencode"]
	selection.Capability.Efforts = []string{"high"}
	profile.ReasoningSelections["opencode"] = selection
	if profileAlreadyApplied(cfg, profile) {
		t.Fatal("changed capability was ignored")
	}
}

func TestReasoningEnglishMenuAndErrors(t *testing.T) {
	input := strings.NewReader("max\n")
	var output bytes.Buffer
	r := &runner{language: languageEnglish, in: input, reader: bufio.NewReader(input), out: &output, errOut: &output}
	c := credentialMaterial{ID: "key", Models: []string{"deepseek-v4-pro"}}
	selected, err := r.selectReasoningEfforts([]string{"opencode"}, []credentialMaterial{c}, map[string]string{"opencode": "key"}, map[string]string{"opencode": "deepseek-v4-pro"}, nil)
	if err != nil || selected["opencode"] != "max" {
		t.Fatalf("selection failed: %v %v", selected, err)
	}
	if !strings.Contains(output.String(), "Maximum reasoning") || strings.Contains(output.String(), "最高推理") {
		t.Fatalf("incorrect locale: %s", &output)
	}
	message := r.localizedErrorMessage(errors.New(`opencode 模型 gpt-5.5 的思考等级 "max" 无效，请编辑方案重新选择`))
	if !strings.Contains(message, "reasoning effort") || !strings.Contains(message, "is invalid") {
		t.Fatalf("error not localized: %s", message)
	}
}
