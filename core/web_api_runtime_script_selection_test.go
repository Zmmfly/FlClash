//go:build webpanel && !android

package core

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
)

func TestWebRuntimeScriptAppliedResolvedMembershipSelectsChangedAndNewGroups(t *testing.T) {
	var selects atomic.Int32
	script := `function main(c){c.proxies=[{name:'New',type:'socks5',server:'192.0.2.2',port:1080}];c['proxy-providers']={I:{type:'inline',payload:[{name:'Remote',type:'socks5',server:'192.0.2.3',port:1080}]}};c['proxy-groups']=[{name:'G',type:'select',proxies:['New','DIRECT'],use:['I']},{name:'NewG',type:'select',proxies:['New']}];return c}`
	f, s := webRuntimeEvaluatedRig(t, script, WebAPIRuntimeBackend{
		State: func(context.Context) (WebAPIRuntimeCoreState, error) {
			id := webEvaluateProfileID
			return WebAPIRuntimeCoreState{Initialized: true, ProfileID: &id, RuntimeGeneration: "7"}, nil
		},
		Apply: func(_ context.Context, p WebAPIRuntimePreparedApply) (WebAPIRuntimeApplyResult, error) {
			return WebAPIRuntimeApplyResult{ActualConfig: "candidate", ProfileID: p.ProfileID, Route: RouteState{CoreEpoch: 7}}, nil
		},
		Proxies: func(context.Context) (ProxiesData, error) {
			return ProxiesData{Proxies: map[string]any{"G": map[string]any{"type": "Selector", "all": []string{"New", "Remote", "DIRECT"}}, "NewG": map[string]any{"type": "Selector", "all": []string{"New"}}}}, nil
		},
		SelectProxy: func(_ context.Context, group, proxy string) (*ChangeProxyResult, error) {
			selects.Add(1)
			return &ChangeProxyResult{Changed: true}, nil
		},
	})
	e := webRuntimeEvaluate(t, f, "2")
	if err := f.service.engine.Close(); err != nil {
		t.Fatal(err)
	}
	apply := webRuntimeApplyBody(t, "apply-script-selection", map[string]any{"expectedRevision": "2", "candidateYaml": string(e.Config), "evaluationId": e.EvaluationID})
	if code, body := s.request(t, "POST", "/api/v1/runtime:apply", apply, s.cookie, ""); code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	if state := s.phase(t, "apply-script-selection"); state.Phase != WebAPIRuntimeApplied {
		t.Fatal(state)
	}
	request := func(revision, op, group, proxy, generation string) (int, []byte) {
		body, _ := json.Marshal(map[string]any{"expectedRevision": revision, "operationId": op, "groupName": group, "proxyName": proxy, "runtimeGeneration": generation})
		return s.request(t, "POST", "/api/v1/runtime/proxies:select", string(body), s.cookie, "")
	}
	for _, bad := range []struct{ op, group, proxy, generation string }{{"script-select-wrong-generation", "G", "New", "8"}, {"script-select-removed-old-base-member", "G", "P", "7"}, {"script-select-ghost-member", "NewG", "Ghost", "7"}} {
		if code, body := request("3", bad.op, bad.group, bad.proxy, bad.generation); code != 409 {
			t.Fatalf("invalid script selection accepted %d %s", code, body)
		}
	}
	if selects.Load() != 0 {
		t.Fatal("rejected script selection entered backend")
	}
	if code, body := request("3", "script-select-new-group", "NewG", "New", "7"); code != 200 {
		t.Fatalf("new script group rejected by old base catalog %d %s", code, body)
	}
	if state := s.phase(t, "script-select-new-group"); state.Phase != WebAPIRuntimeApplied {
		t.Fatal(state)
	}
	if code, body := request("4", "script-select-provider-member", "G", "Remote", "7"); code != 200 {
		t.Fatalf("resolved provider member rejected %d %s", code, body)
	}
	if state := s.phase(t, "script-select-provider-member"); state.Phase != WebAPIRuntimeApplied || selects.Load() != 2 {
		t.Fatalf("selected state %+v count=%d", state, selects.Load())
	}
}
