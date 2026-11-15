//go:build webpanel && !android

package core

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestWebContentModuleManifestMatchesNativeResourceSelection(t *testing.T) {
	cases := []struct {
		name, input string
		values      map[string]string
		resources   []string
		scripts     int
	}{
		{"surge-default", "#!arguments=host:\"example.test\",path:\"one,two.js\"\n[Script]\njob = type=http-request,script-path=https://{{{host}}}/{{{path}}}\n", nil, []string{"https://example.test/one"}, 1},
		{"loon-section-default", "[Argument]\nhost = select,example.test,other.test,tag=Host\npath = input,script.js\n[Script]\nhttp-response .* script-path=https://${host}/{path}\n", nil, []string{"https://example.test/script.js"}, 1},
		{"loon-metadata-default", "#!select=host,example.test,other.test\n[Script]\nhttp-request .* script-path=https://{host}/main.js\n", map[string]string{"host": "override.test"}, []string{"https://override.test/main.js"}, 1},
		{"map-local-text-not-fetch", "[Map Local]\n.* data-type=text data=\"https://not-a-source.test/body\"\n.* data-type=base64 data=\"aHR0cHM6Ly9ub3Qv\"\n.* data-type=file data=body.txt\n", nil, []string{"body.txt"}, 0},
		{"qx-cron-event", "[task_local]\n0 */5 * * * https://example.test/cron.js, tag=cron\nevent-network https://example.test/event.js, tag=network\nevent-start https://unused.test/unsupported.js\n0 1 * * * https://unused.test/disabled.js, enabled=false\n", nil, []string{"https://example.test/cron.js", "https://example.test/event.js"}, 3},
		{"qx-script-local", "^https://example.test url script-response-body local.js\n", nil, []string{"local.js"}, 1},
		{"stash-used-only-and-defaults", "name: Module\narguments:\n  host:\n    type: select\n    options: [example.test, other.test]\nscript-providers:\n  unused: {url: https://unused.test/never.js}\n  active: {url: 'https://${args.host}/active.js'}\n  task: {url: 'https://${args.host}/task.js'}\nhttp:\n  script:\n    - {name: active, type: request, match: '.*'}\n  mock:\n    - {match: '.*', text: 'https://unused.test/not-a-file'}\ncron:\n  script:\n    - {name: task, cron: '0 1 * * *'}\n", nil, []string{"https://example.test/active.js", "https://example.test/task.js"}, 2},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			parsed, err := webContentParseModule([]byte(test.input), test.values)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(parsed.Resources, test.resources) || len(parsed.Scripts) != test.scripts {
				t.Fatalf("resources=%q scripts=%d, want %q/%d", parsed.Resources, len(parsed.Scripts), test.resources, test.scripts)
			}
		})
	}
}

func TestWebContentModuleAndRuleProviderYAMLWorkGuardPrecedesDecode(t *testing.T) {
	// build/api-yaml-budget-contract.md.
	var body strings.Builder
	body.WriteString("payload: []\nhttp: {}\nlarge:\n")
	for i := 0; i < 8300; i++ {
		fmt.Fprintf(&body, "  key%d: value\n", i)
	}
	_, err := webContentParseModule([]byte(body.String()), nil)
	webBackupRequireCode(t, err, "yaml_too_complex")
	_, _, err = webContentRuleSet([]byte(body.String()), true)
	webBackupRequireCode(t, err, "yaml_too_complex")
	cycle := []byte("payload: []\nhttp: {}\ncycle: &loop {again: *loop}\n")
	_, err = webContentParseModule(cycle, nil)
	webBackupRequireCode(t, err, "yaml_too_complex")
	_, _, err = webContentRuleSet(cycle, true)
	webBackupRequireCode(t, err, "yaml_too_complex")
}
