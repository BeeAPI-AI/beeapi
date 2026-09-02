package app

import (
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/BeeAPI-AI/beeapi/internal/beeapi"
)

type agentReasoningAdapter struct {
	Values       []string
	DefaultValue string
}

func reasoningAdapterForAgent(agent string) (agentReasoningAdapter, bool) {
	switch agent {
	case "claude":
		return agentReasoningAdapter{
			Values:       []string{"low", "medium", "high", "xhigh"},
			DefaultValue: "high",
		}, true
	case "codex":
		return agentReasoningAdapter{
			Values:       []string{"minimal", "low", "medium", "high", "xhigh"},
			DefaultValue: "medium",
		}, true
	case "gemini":
		return agentReasoningAdapter{
			Values:       []string{"minimal", "low", "medium", "high"},
			DefaultValue: "medium",
		}, true
	case "grok":
		return agentReasoningAdapter{
			Values:       []string{"low", "medium", "high", "xhigh"},
			DefaultValue: "high",
		}, true
	case "opencode":
		return agentReasoningAdapter{
			Values:       []string{"low", "medium", "high"},
			DefaultValue: "medium",
		}, true
	case "openclaw", "hermes":
		return agentReasoningAdapter{
			Values:       []string{"minimal", "low", "medium", "high", "xhigh"},
			DefaultValue: "medium",
		}, true
	default:
		return agentReasoningAdapter{}, false
	}
}

func credentialModelOptionForID(credential credentialMaterial, model string) (beeapi.ModelOption, bool) {
	for _, option := range credential.ModelOptions {
		if strings.TrimSpace(option.ID) == strings.TrimSpace(model) {
			return option, true
		}
	}
	return beeapi.ModelOption{}, false
}

func modelOptionHasCapability(option beeapi.ModelOption, capability string) bool {
	for _, value := range option.Capabilities {
		if strings.EqualFold(strings.TrimSpace(value), capability) {
			return true
		}
	}
	return false
}

func selectedModelSupportsReasoning(credential credentialMaterial, model string) bool {
	option, found := credentialModelOptionForID(credential, model)
	if found {
		return modelOptionHasCapability(option, "reasoning")
	}
	// Legacy model discovery does not expose capabilities. Preserve the native
	// tool option in that compatibility mode and let the target CLI validate it.
	return !credential.ModelOptionsAuthoritative
}

func valueIndex(values []string, wanted string) int {
	for index, value := range values {
		if strings.EqualFold(value, strings.TrimSpace(wanted)) {
			return index
		}
	}
	return -1
}

func (r *runner) selectReasoningEfforts(agents []string, credentials []credentialMaterial, assignments, models, existing map[string]string) (map[string]string, error) {
	selected := map[string]string{}
	for _, agent := range agents {
		adapter, supported := reasoningAdapterForAgent(agent)
		if !supported {
			continue
		}
		credential, ok := credentialForID(credentials, assignments[agent])
		if !ok {
			return nil, fmt.Errorf(r.text("%s 没有可用的密钥配置", "%s has no usable API Key configuration"), agentLabel(agent))
		}
		if !selectedModelSupportsReasoning(credential, models[agent]) {
			continue
		}

		defaultValue := adapter.DefaultValue
		if valueIndex(adapter.Values, existing[agent]) >= 0 {
			defaultValue = strings.ToLower(strings.TrimSpace(existing[agent]))
		}
		defaultIndex := valueIndex(adapter.Values, defaultValue)
		if defaultIndex < 0 {
			defaultIndex = 0
		}

		r.format(r.out, "\n  %s · %s · 选择思考等级\n", "\n  %s · %s · Choose reasoning effort\n", agentLabel(agent), models[agent])
		for index, value := range adapter.Values {
			labels := make([]string, 0, 2)
			if value == existing[agent] && existing[agent] != "" {
				labels = append(labels, r.text("当前", "Current"))
			}
			if value == adapter.DefaultValue {
				labels = append(labels, r.text("推荐", "Recommended"))
			}
			suffix := ""
			if len(labels) > 0 {
				suffix = " · " + strings.Join(labels, " · ")
			}
			fmt.Fprintf(r.out, "    %d. %s%s\n", index+1, value, suffix)
		}
		for {
			answer, err := r.ask(fmt.Sprintf(r.text("    请选择思考等级 [%d]: ", "    Select reasoning effort [%d]: "), defaultIndex+1))
			if err != nil && !errors.Is(err, io.EOF) {
				return nil, err
			}
			answer = strings.TrimSpace(answer)
			choice := defaultValue
			if answer != "" {
				if number, convErr := strconv.Atoi(answer); convErr == nil {
					if number < 1 || number > len(adapter.Values) {
						r.line(r.errOut, "    思考等级编号无效，请重新选择。", "    Invalid reasoning effort number; try again.")
						continue
					}
					choice = adapter.Values[number-1]
				} else if index := valueIndex(adapter.Values, answer); index >= 0 {
					choice = adapter.Values[index]
				} else {
					r.line(r.errOut, "    思考等级无效，请重新选择。", "    Invalid reasoning effort; try again.")
					continue
				}
			}
			selected[agent] = choice
			r.format(r.out, "    ✓ %s 思考等级 %s\n", "    ✓ %s reasoning effort: %s\n", agentLabel(agent), choice)
			break
		}
	}
	if len(selected) == 0 {
		return nil, nil
	}
	return selected, nil
}
