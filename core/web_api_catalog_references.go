//go:build webpanel && !android

package core

import (
	"errors"
	"net/url"
	"regexp"
	"slices"
	"strings"
)

var webAPIRuleSetToken = regexp.MustCompile(`(?i)(^|\()([\t\n\r ]*RULE-SET[\t\n\r ]*,[\t\n\r ]*)([^,()]+)`)

func webAPIRuleSetTokens(text string) [][2]int {
	var result [][2]int
	for _, indices := range webAPIRuleSetToken.FindAllStringSubmatchIndex(text, -1) {
		start, end := indices[6], indices[7]
		end = start + len(strings.TrimRight(text[start:end], "\t\n\r "))
		if start < end {
			result = append(result, [2]int{start, end})
		}
	}
	return result
}

func webAPIRuleSetTextNames(text string) []string {
	var result []string
	for _, token := range webAPIRuleSetTokens(text) {
		result = append(result, text[token[0]:token[1]])
	}
	return result
}

func webAPIRenameRuleSetText(text string, names map[string]string) (string, error) {
	var result strings.Builder
	last := 0
	for _, token := range webAPIRuleSetTokens(text) {
		name := text[token[0]:token[1]]
		next, ok := names[name]
		if !ok || next == name {
			continue
		}
		if next == "" || strings.TrimSpace(next) != next || strings.ContainsAny(next, ",()\r\n") {
			return "", errors.New("rule provider name cannot round-trip through a rule expression")
		}
		result.WriteString(text[last:token[0]])
		result.WriteString(next)
		last = token[1]
	}
	if last == 0 {
		return text, nil
	}
	result.WriteString(text[last:])
	return result.String(), nil
}

func webAPIRuleSetEntryRange(entry string, policyKey bool) (int, int, bool) {
	const prefix = "rule-set:"
	if len(entry) < len(prefix) || !strings.EqualFold(entry[:len(prefix)], prefix) {
		return 0, 0, false
	}
	start, end := len(prefix), len(entry)
	if !policyKey || strings.Contains(entry[start:], ",") {
		if colon := strings.IndexByte(entry[start:], ':'); colon >= 0 {
			end = start + colon
		}
	}
	return start, end, true
}

func webAPIRuleSetEntryNames(entry string, policyKey bool) []string {
	start, end, ok := webAPIRuleSetEntryRange(entry, policyKey)
	if !ok {
		return nil
	}
	return strings.Split(entry[start:end], ",")
}

func webAPIRenameRuleSetEntry(entry string, names map[string]string, policyKey bool) (string, error) {
	start, end, ok := webAPIRuleSetEntryRange(entry, policyKey)
	if !ok {
		return entry, nil
	}
	before := strings.Split(entry[start:end], ",")
	after := make([]string, len(before))
	for i, name := range before {
		after[i] = name
		if next, exists := names[name]; exists {
			after[i] = next
		}
	}
	if slices.Equal(before, after) {
		return entry, nil
	}
	next := entry[:start] + strings.Join(after, ",") + entry[end:]
	if !slices.Equal(webAPIRuleSetEntryNames(next, policyKey), after) {
		return "", errors.New("rule provider name cannot round-trip through a matcher")
	}
	return next, nil
}

func webAPIDNSProxyNames(server string) ([]string, error) {
	parsed, err := webAPIParseDNSServer(server)
	if err != nil {
		return nil, err
	}
	var result []string
	if webAPIProxyHostScheme(parsed.Scheme) && parsed.Host != "" {
		result = append(result, parsed.Host)
	}
	var dialer string
	for _, part := range strings.Split(parsed.Fragment, "&") {
		if !strings.Contains(part, "=") {
			dialer = part
		}
	}
	if dialer != "" {
		result = append(result, dialer)
	}
	return result, nil
}

func webAPIParseDNSServer(server string) (*url.URL, error) {
	if !strings.Contains(server, "://") {
		server = "udp://" + server
	}
	parsed, err := url.Parse(server)
	if err != nil {
		return nil, errors.New("cannot interpret a stored DNS server reference")
	}
	return parsed, nil
}

func webAPIProxyHostScheme(scheme string) bool {
	switch strings.ToLower(scheme) {
	case "ts", "tailscale", "et", "easytier":
		return true
	default:
		return false
	}
}

func webAPIRenameDNSServer(server string, names map[string]string) (string, error) {
	before, err := webAPIDNSProxyNames(server)
	if err != nil {
		return "", err
	}
	after := make([]string, len(before))
	for i, name := range before {
		after[i] = name
		if next, exists := names[name]; exists {
			after[i] = next
		}
	}
	if slices.Equal(before, after) {
		return server, nil
	}
	parsed, _ := webAPIParseDNSServer(server)
	if webAPIProxyHostScheme(parsed.Scheme) {
		if next, exists := names[parsed.Host]; exists && next != parsed.Host {
			start := strings.Index(server, "://") + 3
			end := strings.IndexAny(server[start:], "/?#")
			if end < 0 {
				end = len(server)
			} else {
				end += start
			}
			server = server[:start] + next + server[end:]
		}
	}
	if hash := strings.IndexByte(server, '#'); hash >= 0 {
		raw := server[hash+1:]
		decoded, starts, ends, err := webAPIDecodeFragment(raw)
		if err != nil {
			return "", err
		}
		start, finish := -1, -1
		for offset := 0; offset <= len(decoded); {
			end := strings.IndexByte(decoded[offset:], '&')
			if end < 0 {
				end = len(decoded)
			} else {
				end += offset
			}
			if !strings.Contains(decoded[offset:end], "=") {
				start, finish = offset, end
			}
			if end == len(decoded) {
				break
			}
			offset = end + 1
		}
		if start >= 0 && start < finish {
			if next, exists := names[decoded[start:finish]]; exists && next != decoded[start:finish] {
				if next == "" || strings.ContainsAny(next, "&=") {
					return "", errors.New("outbound name cannot round-trip through a DNS fragment")
				}
				server = server[:hash+1] + raw[:starts[start]] + url.PathEscape(next) + raw[ends[finish-1]:]
			}
		}
	}
	actual, err := webAPIDNSProxyNames(server)
	if err != nil || !slices.Equal(actual, after) {
		return "", errors.New("outbound name cannot round-trip through a DNS server")
	}
	return server, nil
}

func webAPIDecodeFragment(raw string) (string, []int, []int, error) {
	var decoded strings.Builder
	var starts, ends []int
	for i := 0; i < len(raw); {
		end := i + 1
		piece := raw[i:end]
		if raw[i] == '%' {
			if i+3 > len(raw) {
				return "", nil, nil, errors.New("invalid stored DNS escape")
			}
			end = i + 3
			var err error
			piece, err = url.PathUnescape(raw[i:end])
			if err != nil {
				return "", nil, nil, errors.New("invalid stored DNS escape")
			}
		}
		decoded.WriteString(piece)
		starts = append(starts, i)
		ends = append(ends, end)
		i = end
	}
	return decoded.String(), starts, ends, nil
}
