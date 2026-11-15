//go:build webpanel && !android

package core

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"
)

var webContentModuleAttribute = regexp.MustCompile(`(?:^|[,\s])([\w-]+)\s*=\s*(?:"((?:\\"|[^"])*)"|'([^']*)'|([^,\s]+))`)
var webContentModuleCronField = regexp.MustCompile(`^[\d*?/,\-A-Za-z#]+$`)
var webContentModuleStashArgument = regexp.MustCompile(`(\$?)\$\{args\.(\w+)(\|json)?\}`)

func webContentModuleAttributes(line string) map[string]string {
	result := map[string]string{}
	for _, match := range webContentModuleAttribute.FindAllStringSubmatch(line, -1) {
		value := ""
		for _, candidate := range match[2:] {
			if candidate != "" {
				value = candidate
				break
			}
		}
		result[strings.ToLower(match[1])] = strings.ReplaceAll(value, `\"`, `"`)
	}
	return result
}

func webContentModuleArgumentItems(text string) []string {
	var items []string
	quote := rune(0)
	escaped := false
	start := 0
	for i, c := range text {
		if escaped {
			escaped = false
			continue
		}
		if quote != 0 {
			if c == '\\' && quote == '"' {
				escaped = true
			} else if c == quote {
				quote = 0
			}
		} else if c == '"' || c == '\'' {
			quote = c
		} else if c == ',' {
			items = append(items, strings.TrimSpace(text[start:i]))
			start = i + 1
		}
	}
	return append(items, strings.TrimSpace(text[start:]))
}

func webContentModuleDefaults(lines []string) map[string]string {
	defaults := map[string]string{}
	section := ""
	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.ToLower(strings.TrimSpace(line[1 : len(line)-1]))
			continue
		}
		if strings.HasPrefix(line, "#!") {
			key, value, ok := strings.Cut(strings.TrimPrefix(line, "#!"), "=")
			if !ok {
				continue
			}
			switch strings.ToLower(strings.TrimSpace(key)) {
			case "arguments":
				for _, item := range webContentModuleArgumentItems(value) {
					name, fallback, _ := strings.Cut(item, ":")
					if name = strings.TrimSpace(name); name != "" {
						defaults[name] = strings.Trim(strings.TrimSpace(fallback), "\"'")
					}
				}
			case "input":
				defaults[strings.TrimSpace(value)] = ""
			case "select":
				items := webContentModuleArgumentItems(value)
				if len(items) > 1 {
					defaults[items[0]] = strings.Trim(items[1], "\"'")
				}
			}
			continue
		}
		if section != "argument" || line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") || strings.HasPrefix(line, "//") {
			continue
		}
		name, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		name = strings.TrimSpace(name)
		items := webContentModuleArgumentItems(value)
		if name == "" || len(items) == 0 {
			continue
		}
		kind, fallback := strings.ToLower(items[0]), ""
		if kind == "switch" {
			fallback = "false"
		}
		for _, item := range items[1:] {
			key, _, assigned := strings.Cut(item, "=")
			if assigned && (strings.TrimSpace(key) == "tag" || strings.TrimSpace(key) == "desc" || strings.TrimSpace(key) == "type") {
				continue
			}
			option := strings.Trim(item, "\"'")
			if kind == "switch" && option != "true" && option != "false" {
				continue
			}
			fallback = option
			break
		}
		defaults[name] = fallback
	}
	return defaults
}

func webContentModuleRunnable(kind string) bool {
	switch kind {
	case "http-request", "http-response", "cron", "event", "generic":
		return true
	}
	return false
}

func webContentModuleScript(line, original string) (bool, string) {
	attributes := webContentModuleAttributes(line)
	fields := strings.Fields(line)
	if len(fields) > 0 && !webContentModuleRunnable(strings.ToLower(fields[0])) && fields[0] != "network-changed" && fields[0] != "dns" && fields[0] != "rule" {
		if _, rest, found := strings.Cut(line, "="); found {
			attributes = webContentModuleAttributes(strings.TrimSpace(rest))
		}
	}
	kind := strings.ToLower(attributes["type"])
	if kind == "" && len(fields) > 0 {
		switch strings.ToLower(fields[0]) {
		case "http-request", "http-response", "cron", "event", "dns", "rule", "generic":
			kind = strings.ToLower(fields[0])
		case "network-changed":
			kind = "event"
			attributes["event-name"] = "network-changed"
		default:
			kind = "generic"
		}
	}
	declared := webContentModuleRunnable(kind) || strings.Contains(original, "{")
	if !webContentModuleRunnable(kind) || strings.EqualFold(attributes["enable"], "false") {
		return declared, ""
	}
	if kind == "event" {
		event := attributes["event-name"]
		if event == "" && len(fields) > 1 && strings.EqualFold(fields[0], "event") {
			event = strings.Trim(fields[1], "\"'")
		}
		if event != "network-changed" && event != "engine-started" && event != "profile-reloaded" {
			return declared, ""
		}
	}
	return declared, attributes["script-path"]
}

func webContentModuleQXTask(line string) (bool, string) {
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return false, ""
	}
	start := -1
	if strings.EqualFold(fields[0], "event-network") {
		start = 1
	} else {
		for _, count := range []int{5, 6} {
			if len(fields) <= count {
				continue
			}
			valid := true
			for _, field := range fields[:count] {
				valid = valid && webContentModuleCronField.MatchString(field)
			}
			if valid && (strings.Contains(fields[count], "://") || strings.Contains(fields[count], ".js")) {
				start = count
				break
			}
		}
	}
	if start < 0 {
		return false, ""
	}
	if strings.EqualFold(webContentModuleAttributes(line)["enabled"], "false") {
		return true, ""
	}
	source, _, _ := strings.Cut(fields[start], ",")
	return true, strings.Trim(source, "\"'")
}

// This is the dependency/consent manifest only; Core rule translation is a
// separate runtime responsibility. Unused Stash providers are not dependencies.
func webContentStashManifest(data []byte, values map[string]string) (webContentModule, bool, error) {
	out := webContentModule{}
	var document yaml.Node
	if err := yaml.Unmarshal(data, &document); err != nil || len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return out, false, nil
	}
	if err := WebAPIValidateYAMLWork(&document); err != nil {
		return out, false, err
	}
	var stash map[string]any
	if err := document.Decode(&stash); err != nil {
		return out, false, webAPIError(400, "invalid_module_content", "invalid module YAML")
	}
	recognized := false
	for _, key := range []string{"http", "script-providers", "cron", "rules", "tiles"} {
		_, present := stash[key]
		recognized = recognized || present
	}
	if !recognized {
		return out, false, nil
	}
	scalar := func(value any) string {
		if value == nil {
			return ""
		}
		return fmt.Sprint(value)
	}
	out.Name, out.Description = scalar(stash["name"]), scalar(stash["desc"])
	defaults := map[string]string{}
	types := map[string]string{}
	if declarations, ok := stash["arguments"].(map[string]any); ok {
		for name, raw := range declarations {
			declaration, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			kind := scalar(declaration["type"])
			fallback := ""
			if kind == "switch" {
				fallback = "false"
				types[name] = "boolean"
			}
			if options, ok := declaration["options"].([]any); ok && len(options) > 0 {
				first := options[0]
				if choice, ok := first.(map[string]any); ok {
					first = choice["value"]
				}
				if kind != "switch" {
					fallback = scalar(first)
				}
			}
			if value, present := declaration["default"]; present && value != nil {
				fallback = scalar(value)
			}
			if declaration["value-type"] == "integer" {
				types[name] = "number"
			}
			defaults[name] = fallback
		}
	}
	for key, value := range values {
		defaults[key] = value
	}
	text := func(value any) string {
		return webContentModuleStashArgument.ReplaceAllStringFunc(scalar(value), func(token string) string {
			parts := webContentModuleStashArgument.FindStringSubmatch(token)
			if parts[1] != "" {
				return token[1:]
			}
			value, exists := defaults[parts[2]]
			if !exists {
				return token
			}
			if parts[3] == "" {
				return value
			}
			if types[parts[2]] == "boolean" {
				if value == "true" {
					return "true"
				}
				return "false"
			}
			if types[parts[2]] == "number" && json.Valid([]byte(value)) {
				var number any
				_ = json.Unmarshal([]byte(value), &number)
				if _, ok := number.(float64); ok {
					return value
				}
			}
			encoded, _ := json.Marshal(value)
			return string(encoded)
		})
	}
	providers, _ := stash["script-providers"].(map[string]any)
	resources := map[string]bool{}
	for _, section := range []string{"http", "cron"} {
		object, _ := stash[section].(map[string]any)
		entries, _ := object["script"].([]any)
		for _, raw := range entries {
			entry, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			kind := strings.ToLower(text(entry["type"]))
			active := section == "cron" || kind == "request" || kind == "response"
			if !active && !webContentModuleRunnable(kind) {
				continue
			}
			resolved := map[string]string{}
			for key, value := range entry {
				resolved[key] = text(value)
			}
			body, _ := json.Marshal(struct {
				Declared any
				Resolved map[string]string
			}{entry, resolved})
			out.Scripts = append(out.Scripts, string(body))
			if !active {
				continue
			}
			provider, _ := providers[text(entry["name"])].(map[string]any)
			if source := text(provider["url"]); source != "" {
				resources[source] = true
			}
		}
	}
	for source := range resources {
		out.Resources = append(out.Resources, source)
	}
	return out, true, nil
}
