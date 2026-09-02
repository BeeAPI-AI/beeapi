package configurator

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

type tomlFieldPatch struct {
	Key    string
	Value  string
	Remove bool
}

type tomlSectionPatch struct {
	Name   string
	Fields []tomlFieldPatch
}

func setTOMLField(key, value string) tomlFieldPatch {
	return tomlFieldPatch{Key: key, Value: value}
}

func removeTOMLField(key string) tomlFieldPatch {
	return tomlFieldPatch{Key: key, Remove: true}
}

func patchTOMLFile(path string, topFields []tomlFieldPatch, sections []tomlSectionPatch) error {
	raw, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	text := string(raw)
	newline := detectedNewline(text)
	lines, finalNewline := splitTextLines(text)

	firstSection := len(lines)
	for i, line := range lines {
		if isTOMLHeader(line) {
			firstSection = i
			break
		}
	}
	lines = patchTOMLFields(lines, 0, firstSection, topFields)

	for _, section := range sections {
		var matches []int
		for i, line := range lines {
			name, ok := tomlSectionName(line)
			if ok && canonicalTOMLSectionName(name) == canonicalTOMLSectionName(section.Name) {
				matches = append(matches, i)
			}
		}
		if len(matches) > 1 {
			return fmt.Errorf("TOML 中存在重复的 [%s] 配置段", section.Name)
		}
		if len(matches) == 0 {
			if len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) != "" {
				lines = append(lines, "")
			}
			lines = append(lines, "["+section.Name+"]")
			lines = patchTOMLFields(lines, len(lines), len(lines), section.Fields)
			continue
		}
		start := matches[0] + 1
		end := len(lines)
		for i := start; i < len(lines); i++ {
			if isTOMLHeader(lines[i]) {
				end = i
				break
			}
		}
		lines = patchTOMLFields(lines, start, end, section.Fields)
	}

	return secureWrite(path, []byte(joinTextLines(lines, newline, finalNewline || len(lines) > 0)))
}

func patchTOMLFields(lines []string, start, end int, patches []tomlFieldPatch) []string {
	if len(patches) == 0 {
		return lines
	}
	byKey := make(map[string]tomlFieldPatch, len(patches))
	for _, patch := range patches {
		byKey[patch.Key] = patch
	}
	seen := map[string]bool{}
	replacement := make([]string, 0, end-start+len(patches))
	for _, line := range lines[start:end] {
		key, indent, ok := tomlAssignment(line)
		patch, managed := byKey[key]
		if !ok || !managed {
			replacement = append(replacement, line)
			continue
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		if !patch.Remove {
			replacement = append(replacement, indent+patch.Key+" = "+patch.Value)
		}
	}
	for _, patch := range patches {
		if !seen[patch.Key] && !patch.Remove {
			replacement = append(replacement, patch.Key+" = "+patch.Value)
		}
	}
	result := make([]string, 0, len(lines)-(end-start)+len(replacement))
	result = append(result, lines[:start]...)
	result = append(result, replacement...)
	result = append(result, lines[end:]...)
	return result
}

func tomlAssignment(line string) (key, indent string, ok bool) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "[") {
		return "", "", false
	}
	equals := strings.Index(trimmed, "=")
	if equals <= 0 {
		return "", "", false
	}
	key = strings.TrimSpace(trimmed[:equals])
	if strings.ContainsAny(key, " \t.'\"") {
		return "", "", false
	}
	indent = line[:len(line)-len(strings.TrimLeft(line, " \t"))]
	return key, indent, true
}

func tomlSectionName(line string) (string, bool) {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "[") || strings.HasPrefix(trimmed, "[[") {
		return "", false
	}
	end := strings.Index(trimmed, "]")
	if end < 2 {
		return "", false
	}
	tail := strings.TrimSpace(trimmed[end+1:])
	if tail != "" && !strings.HasPrefix(tail, "#") {
		return "", false
	}
	return strings.TrimSpace(trimmed[1:end]), true
}

func isTOMLHeader(line string) bool {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "[") {
		return false
	}
	if strings.HasPrefix(trimmed, "[[") {
		end := strings.Index(trimmed, "]]")
		if end < 3 {
			return false
		}
		tail := strings.TrimSpace(trimmed[end+2:])
		return tail == "" || strings.HasPrefix(tail, "#")
	}
	_, ok := tomlSectionName(line)
	return ok
}

func canonicalTOMLSectionName(name string) string {
	parts := strings.Split(name, ".")
	for i, part := range parts {
		part = strings.TrimSpace(part)
		if len(part) >= 2 && ((part[0] == '"' && part[len(part)-1] == '"') || (part[0] == '\'' && part[len(part)-1] == '\'')) {
			part = part[1 : len(part)-1]
		}
		parts[i] = part
	}
	return strings.Join(parts, ".")
}

type envFieldPatch struct {
	Key   string
	Value string
}

func patchEnvFile(path string, patches []envFieldPatch) error {
	raw, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, patch := range patches {
		if strings.ContainsAny(patch.Value, "\r\n\x00") {
			return fmt.Errorf("%s 的值包含不允许的换行或空字符", patch.Key)
		}
	}
	text := string(raw)
	newline := detectedNewline(text)
	lines, finalNewline := splitTextLines(text)
	byKey := make(map[string]envFieldPatch, len(patches))
	for _, patch := range patches {
		byKey[patch.Key] = patch
	}
	seen := map[string]bool{}
	result := make([]string, 0, len(lines)+len(patches))
	for _, line := range lines {
		key, ok := dotenvAssignmentKey(line)
		patch, managed := byKey[key]
		if !ok || !managed {
			result = append(result, line)
			continue
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		result = append(result, patch.Key+"="+strconv.Quote(patch.Value))
	}
	missing := 0
	for _, patch := range patches {
		if !seen[patch.Key] {
			missing++
		}
	}
	if missing > 0 && len(result) > 0 && strings.TrimSpace(result[len(result)-1]) != "" {
		result = append(result, "")
	}
	for _, patch := range patches {
		if !seen[patch.Key] {
			result = append(result, patch.Key+"="+strconv.Quote(patch.Value))
		}
	}
	return secureWrite(path, []byte(joinTextLines(result, newline, finalNewline || len(result) > 0)))
}

func dotenvAssignmentKey(line string) (string, bool) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return "", false
	}
	if strings.HasPrefix(trimmed, "export ") {
		trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, "export "))
	}
	equals := strings.Index(trimmed, "=")
	if equals <= 0 {
		return "", false
	}
	key := strings.TrimSpace(trimmed[:equals])
	for _, r := range key {
		if (r < 'A' || r > 'Z') && (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' {
			return "", false
		}
	}
	return key, true
}

type yamlFieldPatch struct {
	Key    string
	Value  string
	Remove bool
}

func patchYAMLMappingFile(path, section string, patches []yamlFieldPatch) error {
	raw, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	text := string(raw)
	newline := detectedNewline(text)
	lines, finalNewline := splitTextLines(text)
	var matches []int
	for i, line := range lines {
		if yamlTopLevelKey(line) == section {
			matches = append(matches, i)
		}
	}
	if len(matches) > 1 {
		return fmt.Errorf("YAML 中存在重复的 %s 配置段", section)
	}
	if len(matches) == 0 {
		if len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) != "" {
			lines = append(lines, "")
		}
		lines = append(lines, section+":")
		for _, patch := range patches {
			if !patch.Remove {
				lines = append(lines, "  "+patch.Key+": "+patch.Value)
			}
		}
		return secureWrite(path, []byte(joinTextLines(lines, newline, true)))
	}

	sectionLine := matches[0]
	afterColon := strings.TrimSpace(strings.SplitN(lines[sectionLine], ":", 2)[1])
	if afterColon != "" && !strings.HasPrefix(afterColon, "#") {
		return fmt.Errorf("YAML 的 %s 使用了行内写法，无法安全局部更新", section)
	}
	end := len(lines)
	for i := sectionLine + 1; i < len(lines); i++ {
		if yamlTopLevelKey(lines[i]) != "" {
			end = i
			break
		}
	}
	indentWidth := 0
	for _, line := range lines[sectionLine+1 : end] {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "-") {
			continue
		}
		width := len(line) - len(strings.TrimLeft(line, " "))
		if width > 0 && (indentWidth == 0 || width < indentWidth) {
			indentWidth = width
		}
	}
	if indentWidth == 0 {
		indentWidth = 2
	}
	indent := strings.Repeat(" ", indentWidth)
	byKey := make(map[string]yamlFieldPatch, len(patches))
	for _, patch := range patches {
		byKey[patch.Key] = patch
	}
	seen := map[string]bool{}
	body := make([]string, 0, end-sectionLine-1+len(patches))
	for _, line := range lines[sectionLine+1 : end] {
		key, width, ok := yamlMappingKey(line)
		patch, managed := byKey[key]
		if !ok || width != indentWidth || !managed {
			body = append(body, line)
			continue
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		if !patch.Remove {
			body = append(body, indent+patch.Key+": "+patch.Value)
		}
	}
	for _, patch := range patches {
		if !seen[patch.Key] && !patch.Remove {
			body = append(body, indent+patch.Key+": "+patch.Value)
		}
	}
	result := make([]string, 0, len(lines)-(end-sectionLine-1)+len(body))
	result = append(result, lines[:sectionLine+1]...)
	result = append(result, body...)
	result = append(result, lines[end:]...)
	return secureWrite(path, []byte(joinTextLines(result, newline, finalNewline || len(result) > 0)))
}

func yamlTopLevelKey(line string) string {
	if line == "" || line[0] == ' ' || line[0] == '\t' || line[0] == '#' || line[0] == '-' {
		return ""
	}
	colon := strings.Index(line, ":")
	if colon <= 0 {
		return ""
	}
	key := strings.TrimSpace(line[:colon])
	if strings.ContainsAny(key, " \t{}[],'\"") {
		return ""
	}
	return key
}

func yamlMappingKey(line string) (key string, indent int, ok bool) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "-") {
		return "", 0, false
	}
	indent = len(line) - len(strings.TrimLeft(line, " "))
	if indent == 0 {
		return "", 0, false
	}
	colon := strings.Index(trimmed, ":")
	if colon <= 0 {
		return "", 0, false
	}
	key = strings.TrimSpace(trimmed[:colon])
	if strings.ContainsAny(key, " \t{}[],'\"") {
		return "", 0, false
	}
	return key, indent, true
}

func detectedNewline(text string) string {
	if strings.Contains(text, "\r\n") {
		return "\r\n"
	}
	return "\n"
}

func splitTextLines(text string) ([]string, bool) {
	if text == "" {
		return nil, false
	}
	normalized := strings.ReplaceAll(text, "\r\n", "\n")
	finalNewline := strings.HasSuffix(normalized, "\n")
	if finalNewline {
		normalized = strings.TrimSuffix(normalized, "\n")
	}
	if normalized == "" {
		return nil, finalNewline
	}
	return strings.Split(normalized, "\n"), finalNewline
}

func joinTextLines(lines []string, newline string, finalNewline bool) string {
	text := strings.Join(lines, newline)
	if finalNewline && (text != "" || len(lines) > 0) {
		text += newline
	}
	return text
}
