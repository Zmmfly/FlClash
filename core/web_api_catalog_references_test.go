//go:build webpanel && !android

package core

import (
	"reflect"
	"testing"
)

func TestWebAPICatalogContractRuleSetReferenceGrammar(t *testing.T) {
	cases := []struct {
		text  string
		names []string
		want  string
	}{
		{"OR,((RULE-SET,a),(RULE-SET,ab)),DIRECT", []string{"a", "ab"}, "OR,((RULE-SET,z),(RULE-SET,ab)),DIRECT"},
		{"SUB-RULE,(( rule-set , a  ),(NETWORK,UDP)),child", []string{"a"}, "SUB-RULE,(( rule-set , z  ),(NETWORK,UDP)),child"},
		{"RULE-SET,a,DIRECT", []string{"a"}, "RULE-SET,z,DIRECT"},
		{"DOMAIN,not-a,Old", nil, "DOMAIN,not-a,Old"},
	}
	for _, tc := range cases {
		t.Run(tc.text, func(t *testing.T) {
			if got := webAPIRuleSetTextNames(tc.text); !reflect.DeepEqual(got, tc.names) {
				t.Fatalf("references = %#v, want %#v", got, tc.names)
			}
			got, err := webAPIRenameRuleSetText(tc.text, map[string]string{"a": "z"})
			if err != nil || got != tc.want {
				t.Fatalf("renamed = %q, %v, want %q", got, err, tc.want)
			}
		})
	}
	for _, name := range []string{"bad,name", "bad(name)", " bad", "bad\nname"} {
		if _, err := webAPIRenameRuleSetText("(RULE-SET,a)", map[string]string{"a": name}); err == nil {
			t.Fatalf("accepted unrepresentable rule-set name %q", name)
		}
	}
}

func TestWebAPICatalogContractRuleSetMatcherGrammar(t *testing.T) {
	cases := []struct {
		text   string
		policy bool
		names  []string
		want   string
	}{
		{"rule-set:a,b:c", true, []string{"a", "b"}, "rule-set:A,b:c"},
		{"RULE-SET:d:e", true, []string{"d:e"}, "RULE-SET:D"},
		{"rule-set:d:e", false, []string{"d"}, "rule-set:d:e"},
		{"geosite:cn", true, nil, "geosite:cn"},
	}
	for _, tc := range cases {
		if got := webAPIRuleSetEntryNames(tc.text, tc.policy); !reflect.DeepEqual(got, tc.names) {
			t.Fatalf("%q references = %#v, want %#v", tc.text, got, tc.names)
		}
		got, err := webAPIRenameRuleSetEntry(tc.text, map[string]string{"a": "A", "d:e": "D"}, tc.policy)
		if err != nil || got != tc.want {
			t.Fatalf("%q renamed = %q, %v, want %q", tc.text, got, err, tc.want)
		}
	}
	if _, err := webAPIRenameRuleSetEntry("rule-set:a,b", map[string]string{"a": "A:B"}, true); err == nil {
		t.Fatal("accepted ambiguous matcher rename")
	}
}

func TestWebAPICatalogContractDNSProxyReferences(t *testing.T) {
	cases := []struct {
		text  string
		names []string
	}{
		{"1.1.1.1", nil},
		{"tls://1.1.1.1#a&ecs=1.2.3.4/24&b", []string{"b"}},
		{"1.1.1.1#Proxy%201", []string{"Proxy 1"}},
		{"1.1.1.1#", nil},
		{"ts://Tail#Out", []string{"Tail", "Out"}},
		{"EasyTier://net/path", []string{"net"}},
		{"1.1.1.1#unused%26ecs=1%26Old", []string{"Old"}},
		{"https://dns.invalid/query#Old%2BOne", []string{"Old+One"}},
	}
	for _, tc := range cases {
		got, err := webAPIDNSProxyNames(tc.text)
		if err != nil || !reflect.DeepEqual(got, tc.names) {
			t.Fatalf("%q references = %#v, %v, want %#v", tc.text, got, err, tc.names)
		}
	}
}

func TestWebAPICatalogContractDNSRenameIsLosslessAndRoundTrips(t *testing.T) {
	cases := []struct{ before, old, next, want string }{
		{"1.1.1.1#Old", "Old", "New, Proxy;1", "1.1.1.1#New%2C%20Proxy%3B1"},
		{"ts://Old/path#Out&ecs=1", "Old", "New", "ts://New/path#Out&ecs=1"},
		{"1.1.1.1#unused%26ecs=1%26Old", "Old", "New", "1.1.1.1#unused%26ecs=1%26New"},
		{"1.1.1.1#Old&ecs=1&Other", "Old", "New", "1.1.1.1#Old&ecs=1&Other"},
		{"https://dns.invalid/query#Old%2BOne", "Old+One", "新节点", "https://dns.invalid/query#%E6%96%B0%E8%8A%82%E7%82%B9"},
	}
	for _, tc := range cases {
		got, err := webAPIRenameDNSServer(tc.before, map[string]string{tc.old: tc.next})
		if err != nil || got != tc.want {
			t.Fatalf("%q renamed = %q, %v, want %q", tc.before, got, err, tc.want)
		}
	}
	for _, name := range []string{"A&B", "A=B", ""} {
		if _, err := webAPIRenameDNSServer("1.1.1.1#Old", map[string]string{"Old": name}); err == nil {
			t.Fatalf("accepted unrepresentable DNS name %q", name)
		}
	}
	if _, err := webAPIRenameDNSServer("ts://Old", map[string]string{"Old": "New/path"}); err == nil {
		t.Fatal("accepted a proxy host rename that changes the parsed host")
	}
}
