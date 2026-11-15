//go:build webpanel && !android

package core

import (
	"bytes"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

type webContentModule struct {
	Name        string
	Description string
	System      *string
	Scripts     []string
	Resources   []string
}

var webContentModuleArgument = regexp.MustCompile(`\{\{\{([^{}]+)\}\}\}|\$\{([^{}]+)\}|\{([^{}]+)\}`)
var webContentQXScript = regexp.MustCompile(`^\S+\s+url\s+script-(?:request-header|request-body|response-header|response-body|echo-response|analyze-echo-response)\s+(\S+)`)

func webContentParseModule(data []byte, values map[string]string) (webContentModule, error) {
	out := webContentModule{}
	if len(data) > WebAPIMaxContentBytes || !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return out, webAPIError(400, "invalid_module_content", "module must be bounded UTF-8 text")
	}
	for key, value := range values {
		if len(key) > 256 || len(value) > 8192 || strings.ContainsAny(key+value, "\x00\r\n") {
			return out, webAPIError(400, "invalid_module_arguments", "invalid module argument")
		}
	}
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	defaults := webContentModuleDefaults(lines)
	for key, value := range values {
		defaults[key] = value
	}
	substitute := func(line string) string {
		return webContentModuleArgument.ReplaceAllStringFunc(line, func(token string) string {
			parts := webContentModuleArgument.FindStringSubmatch(token)
			for _, key := range parts[1:] {
				if key != "" {
					if v, ok := defaults[key]; ok {
						return v
					}
					break
				}
			}
			return token
		})
	}
	resources := map[string]bool{}
	section := ""
	recognized := false
	for _, original := range lines {
		line := strings.TrimSpace(original)
		if strings.HasPrefix(line, "#!") {
			key, value, ok := strings.Cut(strings.TrimPrefix(line, "#!"), "=")
			if ok {
				switch strings.ToLower(strings.TrimSpace(key)) {
				case "name":
					out.Name = strings.TrimSpace(value)
				case "desc":
					out.Description = strings.ReplaceAll(strings.TrimSpace(value), `\n`, "\n")
				case "system":
					value = strings.TrimSpace(value)
					out.System = &value
				}
			}
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.ToLower(strings.TrimSpace(line[1 : len(line)-1]))
			recognized = true
			continue
		}
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") || strings.HasPrefix(line, "//") {
			continue
		}
		line = substitute(line)
		if section == "script" {
			declared, source := webContentModuleScript(line, original)
			if declared {
				out.Scripts = append(out.Scripts, original+"\x00"+line)
			}
			if source != "" {
				resources[source] = true
			}
		}
		if section == "map local" || section == "rewrite" {
			attributes := webContentModuleAttributes(line)
			dataType := strings.ToLower(attributes["data-type"])
			if source := attributes["data-path"]; source != "" && dataType != "text" && dataType != "base64" {
				resources[source] = true
			}
			if section == "map local" && (dataType == "" || dataType == "file") {
				if source := attributes["data"]; source != "" {
					resources[source] = true
				}
			}
		}
		if section == "" || section == "task_local" || section == "rewrite_local" {
			if match := webContentQXScript.FindStringSubmatch(line); len(match) > 1 {
				out.Scripts = append(out.Scripts, original+"\x00"+line)
				resources[match[1]] = true
				recognized = true
			}
			if declared, source := webContentModuleQXTask(line); declared {
				out.Scripts = append(out.Scripts, original+"\x00"+line)
				if source != "" {
					resources[source] = true
				}
				recognized = true
			}
		}
		if strings.HasPrefix(strings.ToLower(line), "hostname") || strings.Contains(line, " url ") {
			recognized = true
		}
	}
	if section == "" {
		stash, found, err := webContentStashManifest(data, values)
		if err != nil {
			return out, err
		}
		if found {
			out = stash
			recognized = true
			for _, source := range out.Resources {
				resources[source] = true
			}
			out.Resources = nil
		}
	}
	if !recognized && strings.TrimSpace(string(data)) != "" {
		return out, webAPIError(400, "invalid_module_content", "unrecognized module document")
	}
	if len(resources) > 1000 || len(out.Scripts) > 10000 {
		return out, webAPIError(413, "module_complexity_limit", "module has too many resources or scripts")
	}
	for source := range resources {
		if len(source) > 8192 || strings.ContainsAny(source, "\x00\r\n") {
			return out, webAPIError(400, "invalid_module_resource", "invalid module resource reference")
		}
		out.Resources = append(out.Resources, source)
	}
	sort.Strings(out.Resources)
	sort.Strings(out.Scripts)
	return out, nil
}

func webContentModuleResourceKey(data []byte) string { return webContentDigest(data)[:32] }

func webContentModuleFirstSource(module WebAPIMitmModule, name string) string {
	var first string
	for source, key := range module.Resources {
		if key == name && (strings.HasPrefix(source, "http://") || strings.HasPrefix(source, "https://")) && (first == "" || source < first) {
			first = source
		}
	}
	return first
}

func webContentModuleHasKey(module WebAPIMitmModule, key string) bool {
	for _, known := range module.Resources {
		if known == key {
			return true
		}
	}
	return false
}

func webContentModuleScriptDigest(module webContentModule, resources map[string]string) string {
	var parts strings.Builder
	for _, line := range module.Scripts {
		parts.WriteString(line)
		parts.WriteByte(0)
	}
	for _, source := range module.Resources {
		parts.WriteString(source)
		parts.WriteByte(0)
		parts.WriteString(resources[source])
		parts.WriteByte(0)
	}
	return webContentDigest([]byte(parts.String()))
}
