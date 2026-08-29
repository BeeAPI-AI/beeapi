package app

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/BeeAPI-AI/beeapi/internal/state"
)

const (
	languageChinese = "zh-CN"
	languageEnglish = "en"
)

func normalizeLanguage(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, "_", "-")
	switch value {
	case "zh", "zh-cn", "zh-hans", "cn", "chinese", "简体中文", "中文", "1":
		return languageChinese
	case "en", "en-us", "en-gb", "english", "2":
		return languageEnglish
	default:
		return ""
	}
}

func preferredLanguage() string {
	if language := normalizeLanguage(os.Getenv("BEEAPI_LANG")); language != "" {
		return language
	}
	for _, key := range []string{"LC_ALL", "LC_MESSAGES", "LANGUAGE", "LANG"} {
		value := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
		if value == "" || value == "c" || value == "posix" || strings.HasPrefix(value, "c.") {
			continue
		}
		if strings.HasPrefix(value, "zh") {
			return languageChinese
		}
		return languageEnglish
	}
	// BeeAPI's existing users are predominantly Chinese-speaking. Keeping this
	// fallback also makes headless/minimal Linux installs deterministic.
	return languageChinese
}

func (r *runner) text(chinese, english string) string {
	if r.language == languageEnglish {
		return english
	}
	return chinese
}

func (r *runner) line(out io.Writer, chinese, english string) {
	fmt.Fprintln(out, r.text(chinese, english))
}

func (r *runner) format(out io.Writer, chinese, english string, args ...any) {
	fmt.Fprintf(out, r.text(chinese, english), args...)
}

func (r *runner) askLocalized(chinese, english string) (string, error) {
	return r.ask(r.text(chinese, english))
}

func (r *runner) localizedHint(message string) string {
	if r.language != languageEnglish {
		return message
	}
	if message == "旧版 Shell 命令注入已移除；重新打开终端后生效" {
		return "Legacy Shell command injection was removed; reopen the terminal to apply the change"
	}
	if strings.HasPrefix(message, "仅更新 BeeAPI 连接字段；原配置已完整备份，可用 beeapi rollback ") && strings.HasSuffix(message, " 恢复") {
		id := strings.TrimSuffix(strings.TrimPrefix(message, "仅更新 BeeAPI 连接字段；原配置已完整备份，可用 beeapi rollback "), " 恢复")
		return "Only BeeAPI connection fields were updated; the original configuration was fully backed up. Restore it with beeapi rollback " + id
	}
	if message == "Claude Desktop: 直接打开 Code 标签页" {
		return "Claude Desktop: open the Code tab directly"
	}
	return message
}

func (r *runner) localizedModelLabel(message string) string {
	if r.language != languageEnglish {
		return message
	}
	message = strings.ReplaceAll(message, "默认候选", "Default candidate")
	message = strings.ReplaceAll(message, "客户端适配", "Client adapter")
	message = strings.ReplaceAll(message, "沿用当前可用模型", "Keep current compatible model")
	return message
}

func (r *runner) localizedErrorMessage(err error) string {
	if err == nil {
		return ""
	}
	message := err.Error()
	if r.language != languageEnglish {
		return message
	}
	exact := map[string]string{
		"没有可用模型":            "No available models",
		"模型协议元数据不完整":        "Model protocol metadata is incomplete",
		"API Key 为空":        "API Key is empty",
		"BeeAPI 入口无效":       "Invalid BeeAPI endpoint",
		"无法读取余额":            "Could not load balance",
		"配置方案信息不完整":         "Profile information is incomplete",
		"配置方案的 BeeAPI 入口无效": "The profile's BeeAPI endpoint is invalid",
		"配置方案至少需要一个 AI 工具":  "A profile requires at least one AI tool",
		"配置方案名称不能为空":        "Profile name is required",
		"配置方案名称不能超过 40 个字符": "Profile name cannot exceed 40 characters",
		"配置方案名称不能包含控制字符":    "Profile name cannot contain control characters",
	}
	if translated := exact[message]; translated != "" {
		return translated
	}
	message = strings.ReplaceAll(message, "BeeAPI 返回", "BeeAPI returned")
	message = strings.ReplaceAll(message, "[已隐藏]", "[redacted]")
	message = strings.ReplaceAll(message, "该 API Key 没有支持 ", "This API Key has no model supporting ")
	message = strings.ReplaceAll(message, " 的模型，请选择其他 Key", "; choose another Key")
	message = strings.ReplaceAll(message, "没有对应的本地凭据", "has no matching local credential")
	message = strings.ReplaceAll(message, "尚未选择 API Key", "has no selected API Key")
	message = strings.ReplaceAll(message, "尚未选择模型", "has no selected model")
	return message
}

func (r *runner) chooseLanguage(suggested string) (string, error) {
	if normalizeLanguage(suggested) == "" {
		suggested = languageChinese
	}
	defaultChoice := "1"
	if suggested == languageEnglish {
		defaultChoice = "2"
	}
	fmt.Fprintln(r.out, "\nChoose your language / 选择语言")
	fmt.Fprintln(r.out, "  1. 简体中文")
	fmt.Fprintln(r.out, "  2. English")
	for {
		answer, err := r.ask(fmt.Sprintf("\n请选择 / Select [%s]: ", defaultChoice))
		if errors.Is(err, io.EOF) && strings.TrimSpace(answer) == "" {
			return suggested, nil
		}
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(answer) == "" {
			return suggested, nil
		}
		if language := normalizeLanguage(answer); language != "" {
			return language, nil
		}
		fmt.Fprintln(r.errOut, "  请输入 1 或 2 / Please enter 1 or 2.")
	}
}

func (r *runner) initializeLanguage(cfg *state.Config, prompt bool) error {
	if language := normalizeLanguage(cfg.Language); language != "" {
		r.language = language
		return nil
	}
	suggested := preferredLanguage()
	selected := suggested
	if prompt && !cfg.Initialized() {
		r.showLogo()
		var err error
		selected, err = r.chooseLanguage(suggested)
		if err != nil {
			return err
		}
	} else if !cfg.Initialized() {
		// Non-interactive commands such as help and token print must never consume
		// input or mark the one-time language choice as completed.
		r.language = selected
		return nil
	}
	r.language = selected
	cfg.Language = selected
	return r.store.SaveConfig(*cfg)
}

func (r *runner) languageMenu() error {
	cfg, err := r.store.LoadConfig()
	if err != nil {
		return err
	}
	current := r.text("简体中文", "English")
	r.format(r.out, "\n界面语言 · 当前为 %s\n", "\nInterface language · Current: %s\n", current)
	fmt.Fprintln(r.out, "  1. 简体中文")
	fmt.Fprintln(r.out, "  2. English")
	answer, askErr := r.askLocalized("请选择，输入 0 返回: ", "Select a language, or enter 0 to go back: ")
	if askErr != nil && !errors.Is(askErr, io.EOF) {
		return askErr
	}
	answer = strings.TrimSpace(answer)
	if answer == "" || answer == "0" {
		return nil
	}
	language := normalizeLanguage(answer)
	if language == "" {
		return errors.New(r.text("语言选项无效", "Invalid language selection"))
	}
	cfg.Language = language
	if err := r.store.SaveConfig(cfg); err != nil {
		return err
	}
	r.language = language
	r.line(r.out, "✓ 已切换为简体中文。", "✓ Language changed to English.")
	return nil
}

func defaultProfileNameForLanguage(language string) string {
	if normalizeLanguage(language) == languageEnglish {
		return "Default"
	}
	return defaultProfileName
}

// ErrorPrefix returns the localized label used by the thin main package after
// Run returns an error. It intentionally never prompts.
func ErrorPrefix() string {
	language := preferredLanguage()
	if store, err := state.Open(); err == nil {
		if cfg, loadErr := store.LoadConfig(); loadErr == nil && normalizeLanguage(cfg.Language) != "" {
			language = normalizeLanguage(cfg.Language)
		}
	}
	if language == languageEnglish {
		return "Error:"
	}
	return "错误:"
}
