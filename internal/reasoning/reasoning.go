// Package reasoning owns the model/route capability rules shared by the menu
// and native configuration writers. It never infers support from a model name
// containing words such as "max", "thinking", or "reasoner".
package reasoning

import (
	"slices"
	"strings"
	"time"
)

const (
	Messages  = "anthropic/messages"
	Responses = "openai/responses"
	Chat      = "openai/chat_completions"
	Contents  = "gemini/contents"
)

// Capability is the optional, protocol-scoped model-options contract. Values
// describe effective native levels, not compatibility aliases. An explicit
// mode=none, an empty object, or an invalid object disables manual selection.
// Budgets maps UI level names to native thinkingBudget values (Gemini only).
type Capability struct {
	Mode    string         `json:"mode"`
	Efforts []string       `json:"supported_efforts,omitempty"`
	Default string         `json:"default_effort,omitempty"`
	Budgets map[string]int `json:"budgets,omitempty"`
}

// Selection binds a capability snapshot to the selected model and protocol so
// saved plans can be applied offline without widening route permissions.
type Selection struct {
	Model      string     `json:"model"`
	Protocol   string     `json:"protocol"`
	Capability Capability `json:"capability"`
}

func Protocol(agent string) string {
	switch agent {
	case "claude", "claude-desktop":
		return Messages
	case "codex", "grok", "openclaw":
		return Responses
	case "gemini":
		return Contents
	case "opencode", "hermes":
		return Chat
	}
	return ""
}

var orderedEfforts = []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}

func SupportsAgent(agent string) bool {
	return Protocol(agent) != "" && agent != "claude-desktop"
}

// ToolValues describes fields the native adapter can actually express. Codex
// max additionally needs a local runtime check before offering or writing it.
func ToolValues(agent string) []string {
	switch agent {
	case "claude":
		return []string{"low", "medium", "high", "xhigh", "max"}
	case "gemini":
		return []string{"minimal", "low", "medium", "high"}
	case "grok":
		return []string{"low", "medium", "high", "xhigh"}
	case "codex":
		return []string{"minimal", "low", "medium", "high", "xhigh", "max"}
	case "opencode", "openclaw", "hermes":
		return slices.Clone(orderedEfforts)
	}
	return nil
}

func KnownValue(value string) bool { return slices.Contains(orderedEfforts, value) }

// Normalize rejects the whole declaration on malformed/unknown values rather
// than guessing which part of a new protocol the old CLI can safely use.
func Normalize(protocol string, c Capability) Capability {
	if c.Mode == "none" {
		return Capability{Mode: "none"}
	}
	if (c.Mode != "effort" || protocol == Contents) &&
		(c.Mode != "thinking_level" || protocol != Contents) &&
		(c.Mode != "budget" || protocol != Contents) {
		return Capability{Mode: "none"}
	}
	if len(c.Efforts) == 0 || len(c.Efforts) > len(orderedEfforts) {
		return Capability{Mode: "none"}
	}
	seen := map[string]bool{}
	for _, value := range c.Efforts {
		if !KnownValue(value) || seen[value] {
			return Capability{Mode: "none"}
		}
		seen[value] = true
		if c.Mode == "budget" {
			budget, ok := c.Budgets[value]
			if !ok || budget < 0 || budget > 32768 {
				return Capability{Mode: "none"}
			}
		}
	}
	if c.Default != "" && !seen[c.Default] {
		return Capability{Mode: "none"}
	}
	clean := Capability{Mode: c.Mode, Default: c.Default}
	for _, value := range orderedEfforts {
		if seen[value] {
			clean.Efforts = append(clean.Efforts, value)
		}
	}
	if c.Mode == "budget" {
		clean.Budgets = map[string]int{}
		for _, value := range clean.Efforts {
			clean.Budgets[value] = c.Budgets[value]
		}
	}
	return clean
}

func Resolve(agent, model string, snapshot *Selection) Capability {
	protocol := Protocol(agent)
	if !SupportsAgent(agent) {
		return Capability{Mode: "none"}
	}
	if snapshot != nil {
		if snapshot.Model != model || snapshot.Protocol != protocol {
			return Capability{Mode: "none"}
		}
		return Normalize(protocol, snapshot.Capability)
	}
	return Normalize(protocol, Fallback(model, protocol))
}

func Values(agent string, capability Capability) []string {
	var values []string
	for _, value := range capability.Efforts {
		if slices.Contains(ToolValues(agent), value) {
			values = append(values, value)
		}
	}
	return values
}

func Default(capability Capability, values []string) string {
	for _, value := range []string{capability.Default, "medium", "high", "low"} {
		if slices.Contains(values, value) {
			return value
		}
	}
	if len(values) != 0 {
		return values[0]
	}
	return ""
}

func Clone(source map[string]Selection) map[string]Selection {
	if len(source) == 0 {
		return nil
	}
	result := make(map[string]Selection, len(source))
	for key, value := range source {
		value.Capability = Normalize(value.Protocol, value.Capability)
		result[key] = value
	}
	return result
}

// Fallback is deliberately a bounded, documented catalogue (2026-09-10).
// Dated snapshots of known families are allowed; unknown suffixes, merchant
// aliases, future versions and cross-protocol translations need route metadata.
func Fallback(model, protocol string) Capability {
	id := strings.ToLower(strings.TrimSpace(model))
	if namespace, name, ok := strings.Cut(id, "/"); ok {
		prefixes := map[string][]string{"openai": {"gpt-", "o3", "o4-"}, "anthropic": {"claude-"}, "google": {"gemini-"}, "xai": {"grok-"}, "deepseek": {"deepseek-"}, "zai": {"glm-"}, "moonshot": {"kimi-"}, "qwen": {"qwen"}}
		known := false
		for _, prefix := range prefixes[namespace] {
			if strings.HasPrefix(name, prefix) {
				known = true
			}
		}
		if !known {
			return Capability{Mode: "none"}
		}
		id = name
	}
	effort := func(def string, values ...string) Capability {
		return Capability{Mode: "effort", Default: def, Efforts: values}
	}
	if protocol == Chat || protocol == Responses {
		if matches(id, "gpt-6-astra") {
			return effort("medium", "low", "medium", "high", "xhigh", "max")
		}
		if matches(id, "gpt-5.6", "gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna") {
			return effort("medium", "none", "low", "medium", "high", "xhigh", "max")
		}
		if matches(id, "gpt-5.5", "gpt-5.4", "gpt-5.4-mini", "gpt-5.4-nano", "gpt-5.2") {
			return effort("medium", "none", "low", "medium", "high", "xhigh")
		}
		if protocol == Responses && matches(id, "gpt-5.3-codex", "gpt-5.3-codex-spark", "gpt-5.2-codex", "gpt-5.1-codex-max") {
			return effort("medium", "low", "medium", "high", "xhigh")
		}
		if matches(id, "gpt-5.1") {
			return effort("medium", "none", "low", "medium", "high")
		}
		if matches(id, "gpt-5", "gpt-5-mini", "gpt-5-nano") {
			return effort("medium", "minimal", "low", "medium", "high")
		}
		if matches(id, "o3", "o3-mini", "o4-mini") {
			return effort("medium", "low", "medium", "high")
		}
		if matches(id, "grok-4.6") {
			return effort("high", "low", "medium", "high", "xhigh")
		}
		if matches(id, "grok-4.5") {
			return effort("high", "low", "medium", "high")
		}
		if matches(id, "grok-4.3") {
			return effort("high", "none", "low", "medium", "high")
		}
	}
	if protocol == Chat || protocol == Responses || protocol == Messages {
		if matches(id, "deepseek-v4-pro", "deepseek-v4-flash") {
			return effort("high", "low", "high", "max")
		}
	}
	if protocol == Messages {
		if matches(id, "claude-opus-4-6", "claude-opus-4.6", "claude-sonnet-4-6", "claude-sonnet-4.6", "claude-mythos-preview") {
			return effort("high", "low", "medium", "high", "max")
		}
		if matches(id, "claude-opus-4-7", "claude-opus-4.7", "claude-opus-4-8", "claude-opus-4.8", "claude-opus-5", "claude-sonnet-5", "claude-fable-5", "claude-fable-5-1", "claude-fable-5.1", "claude-mythos-5", "claude-mythos-5-1", "claude-mythos-5.1") {
			return effort("high", "low", "medium", "high", "xhigh", "max")
		}
		if matches(id, "claude-opus-4-5", "claude-opus-4.5") {
			return effort("high", "low", "medium", "high")
		}
	}
	if protocol == Chat {
		if matches(id, "glm-5.2") {
			return effort("high", "high", "max")
		}
		if matches(id, "kimi-k3") {
			return effort("high", "low", "high", "max")
		}
		if matches(id, "qwen3.8-max", "qwen3.8-flash") {
			return effort("medium", "low", "medium", "xhigh")
		}
	}
	if protocol == Contents {
		level := func(def string, values ...string) Capability {
			return Capability{Mode: "thinking_level", Default: def, Efforts: values}
		}
		if matches(id, "gemini-3-pro-preview") {
			return level("high", "low", "high")
		}
		if matches(id, "gemini-3.1-pro-preview", "gemini-3.7-flash", "gemini-3.8-flash") {
			return level("medium", "low", "medium", "high")
		}
		if matches(id, "gemini-3-flash-preview", "gemini-3.5-flash", "gemini-3.5-flash-lite", "gemini-3.6-flash") {
			return level("medium", "minimal", "low", "medium", "high")
		}
		if matches(id, "gemini-2.5-pro") {
			return Capability{Mode: "budget", Default: "medium", Efforts: []string{"minimal", "low", "medium", "high"}, Budgets: map[string]int{"minimal": 128, "low": 1024, "medium": 8192, "high": 32768}}
		}
		if matches(id, "gemini-2.5-flash", "gemini-2.5-flash-lite") {
			return Capability{Mode: "budget", Default: "medium", Efforts: []string{"minimal", "low", "medium", "high"}, Budgets: map[string]int{"minimal": 0, "low": 1024, "medium": 8192, "high": 24576}}
		}
	}
	return Capability{Mode: "none"}
}

func matches(id string, names ...string) bool {
	for _, name := range names {
		if id == name {
			return true
		}
		if !strings.HasPrefix(id, name+"-") {
			continue
		}
		date := strings.TrimPrefix(id, name+"-")
		// YYYYMMDD or YYYY-MM-DD only; never guess what '-pro' or '-max' means.
		if len(date) == 10 && date[4] == '-' && date[7] == '-' {
			date = strings.ReplaceAll(date, "-", "")
		}
		if len(date) != 8 {
			continue
		}
		if _, err := time.Parse("20060102", date); err == nil {
			return true
		}
	}
	return false
}
