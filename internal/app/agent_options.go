package app

import (
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/BeeAPI-AI/beeapi/internal/beeapi"
	"github.com/BeeAPI-AI/beeapi/internal/reasoning"
)

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

func selectionForModel(agent, model string, credential credentialMaterial) reasoning.Selection {
	protocol := reasoning.Protocol(agent)
	capability := reasoning.Fallback(model, protocol)
	option, found := credentialModelOptionForID(credential, model)
	if found && option.Reasoning != nil {
		// An explicit protocol map is authoritative, including missing entries.
		capability = option.Reasoning[protocol]
	} else if (found && !modelOptionHasCapability(option, "reasoning")) || (!found && credential.ModelOptionsAuthoritative) {
		capability = reasoning.Capability{Mode: "none"}
	}
	return reasoning.Selection{Model: model, Protocol: protocol, Capability: reasoning.Normalize(protocol, capability)}
}

func reasoningSelections(agents []string, credentials []credentialMaterial, assignments, models map[string]string) map[string]reasoning.Selection {
	selections := make(map[string]reasoning.Selection, len(agents))
	for _, agent := range agents {
		if credential, ok := credentialForID(credentials, assignments[agent]); ok {
			selections[agent] = selectionForModel(agent, models[agent], credential)
		}
	}
	return selections
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
		if !reasoning.SupportsAgent(agent) {
			continue
		}
		credential, ok := credentialForID(credentials, assignments[agent])
		if !ok {
			return nil, fmt.Errorf(r.text("%s 没有可用的密钥配置", "%s has no usable API Key configuration"), agentLabel(agent))
		}
		snapshot := selectionForModel(agent, models[agent], credential)
		capability := reasoning.Resolve(agent, models[agent], &snapshot)
		values := reasoning.Values(agent, capability)
		if agent == "codex" && valueIndex(values, "max") >= 0 && !r.codexAcceptsMax() {
			values = removeReasoningValue(values, "max")
			r.line(r.out, "  本机 Codex 尚未确认支持 max 配置，已隐藏此档位；更新 Codex 后可重新选择。", "  This Codex runtime has not confirmed max support; the level is hidden. Update Codex and select it again.")
		}
		if len(values) == 0 {
			r.format(r.out, "  %s · %s：未确认可设置的推理档位，使用工具/模型默认行为。\n", "  %s · %s: no verified reasoning control; using tool/model defaults.\n", agentLabel(agent), models[agent])
			continue
		}

		recommended := reasoning.Default(capability, values)
		defaultValue := recommended
		if valueIndex(values, existing[agent]) >= 0 {
			defaultValue = strings.ToLower(strings.TrimSpace(existing[agent]))
		} else if existing[agent] != "" {
			r.format(r.out, "  之前的 %s 档位不适用于当前模型/工具，请重新选择。\n", "  The previous %s level is not valid for this model/tool; choose again.\n", existing[agent])
		}
		defaultIndex := valueIndex(values, defaultValue)
		if defaultIndex < 0 {
			defaultIndex = 0
		}

		r.format(r.out, "\n  %s · %s · 选择思考等级\n", "\n  %s · %s · Choose reasoning effort\n", agentLabel(agent), models[agent])
		if capability.Mode == "budget" {
			r.line(r.out, "  此模型使用思考 Token 预算，以下为预算预设，不是 effort 档位。", "  This model uses thinking-token budgets; these are budget presets, not effort levels.")
		}
		for index, value := range values {
			labels := make([]string, 0, 2)
			if value == existing[agent] && existing[agent] != "" {
				labels = append(labels, r.text("当前", "Current"))
			}
			if value == recommended {
				labels = append(labels, r.text("推荐", "Recommended"))
			}
			if capability.Mode == "budget" {
				labels = append(labels, fmt.Sprintf("%d tokens", capability.Budgets[value]))
			} else if value == "max" {
				labels = append(labels, r.text("最高推理 · 耗时和用量更高", "Maximum reasoning · higher latency and usage"))
			} else if len(capability.Efforts) > 0 && value == capability.Efforts[len(capability.Efforts)-1] {
				labels = append(labels, r.text("此模型最高", "Model maximum"))
			}
			suffix := ""
			if len(labels) > 0 {
				suffix = " · " + strings.Join(labels, " · ")
			}
			fmt.Fprintf(r.out, "    %d. %s%s\n", index+1, value, suffix)
		}
		r.line(r.out, "    0. 使用工具/模型默认行为（不设置推理档位）", "    0. Use tool/model defaults (no reasoning override)")
		for {
			answer, err := r.ask(fmt.Sprintf(r.text("    请选择思考等级 [%d]: ", "    Select reasoning effort [%d]: "), defaultIndex+1))
			if err != nil && !errors.Is(err, io.EOF) {
				return nil, err
			}
			answer = strings.TrimSpace(answer)
			if answer == "0" || strings.EqualFold(answer, "auto") {
				break
			}
			choice := defaultValue
			if answer != "" {
				if number, convErr := strconv.Atoi(answer); convErr == nil {
					if number < 1 || number > len(values) {
						r.line(r.errOut, "    思考等级编号无效，请重新选择。", "    Invalid reasoning effort number; try again.")
						continue
					}
					choice = values[number-1]
				} else if index := valueIndex(values, answer); index >= 0 {
					choice = values[index]
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

func removeReasoningValue(values []string, excluded string) []string {
	filtered := make([]string, 0, len(values))
	for _, value := range values {
		if value != excluded {
			filtered = append(filtered, value)
		}
	}
	return filtered
}

func (r *runner) codexAcceptsMax() bool {
	if r.codexMaxSupport != nil {
		return r.codexMaxSupport()
	}
	return reasoning.CodexSupportsMax(r.ctx)
}

// Non-interactive reconfiguration may change the model or Key. Preserve only
// still-valid explicit choices; never silently translate max to high.
func (r *runner) retainedReasoningEfforts(agents []string, models, existing map[string]string, selections map[string]reasoning.Selection) map[string]string {
	retained := map[string]string{}
	for _, agent := range agents {
		value := existing[agent]
		if value == "" {
			continue
		}
		snapshot := selections[agent]
		values := reasoning.Values(agent, reasoning.Resolve(agent, models[agent], &snapshot))
		if valueIndex(values, value) >= 0 && (agent != "codex" || value != "max" || r.codexAcceptsMax()) {
			retained[agent] = value
		} else {
			r.format(r.out, "  %s 的原推理档位 %s 不适用于当前配置，已清除；可在编辑方案中重新选择。\n", "  %s's previous reasoning level %s is not valid for this configuration and was cleared; edit the configuration to select another.\n", agentLabel(agent), value)
		}
	}
	return retained
}
