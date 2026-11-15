//go:build webpanel && !android

package core

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/metacubex/wazero"
)

func webEvaluateTestEngine(t *testing.T) *webEvaluateEngine {
	t.Helper()
	engine, err := newWebEvaluateEngine(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { engine.Close() })
	return engine
}

func TestWebEvaluateGuestHasNoImportsAndHardMemoryMaximum(t *testing.T) {
	// build/api-evaluate-contract.md: no WASI, host functions, imported tables or globals.
	for offset := 8; offset < len(webEvaluateGuest); {
		kind := webEvaluateGuest[offset]
		offset++
		size, n := binary.Uvarint(webEvaluateGuest[offset:])
		if n <= 0 || size > uint64(len(webEvaluateGuest)-offset-n) {
			t.Fatal("invalid guest section")
		}
		offset += n
		if kind == 2 {
			count, n := binary.Uvarint(webEvaluateGuest[offset:])
			if n <= 0 || count != 0 {
				t.Fatal("guest imports host capabilities")
			}
		}
		offset += int(size)
	}
	engine := webEvaluateTestEngine(t)
	module, err := engine.runtime.InstantiateModule(context.Background(), engine.compiled, wazero.NewModuleConfig().WithName("").WithStartFunctions("_initialize"))
	if err != nil {
		t.Fatal(err)
	}
	defer module.Close(context.Background())
	if _, ok := module.Memory().Grow(webEvaluateMemoryPages); ok {
		t.Fatal("guest escaped the exact 256 MiB page maximum")
	}
}

func TestWebEvaluateNativeLanguageContract(t *testing.T) {
	engine := webEvaluateTestEngine(t)
	cases := []struct{ name, script, want string }{
		{"function", "function main(c,n){c.name=n;c.mode='global';return c}", `{"mode":"global","name":"original label"}`},
		{"lexical", "const main=(c)=>({...c, first:c.proxies?.[0]?.name??null, values:[...new Set([1,1,2])], map:Object.fromEntries(new Map([['a',1]]))})", `{"mode":"rule","first":null,"values":[1,2],"map":{"a":1}}`},
		{"null-original", "function main(c){c.mode='changed';return null}", `{"mode":"rule"}`},
		{"undefined-original", "function main(c){c.mode='changed'}", `{"mode":"rule"}`},
		{"promise", "async function main(c){await Promise.resolve();return {...c, mode:'global'}}", `{"mode":"global"}`},
		{"console", "function main(c){for(const k of ['log','info','warn','error','debug','trace'])console[k]('hello',{x:1});return c}", `{"mode":"rule"}`},
		{"console-unserializable", "function main(c){const cycle={};cycle.x=cycle;console.log(Symbol('s'),cycle,{toJSON(){throw new Error('synthetic')}});return c}", `{"mode":"rule"}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			result, err := engine.Evaluate(context.Background(), []byte(c.script), []byte(`{"mode":"rule"}`), "original label")
			if err != nil {
				t.Fatal(err)
			}
			var got, want any
			if json.Unmarshal(result.JSON, &got) != nil || json.Unmarshal([]byte(c.want), &want) != nil {
				t.Fatalf("invalid result %s", result.JSON)
			}
			g, _ := json.Marshal(got)
			w, _ := json.Marshal(want)
			if string(g) != string(w) {
				t.Fatalf("got %s; want %s", g, w)
			}
			if c.name == "console" && strings.Count(result.Console, "hello") != 6 {
				t.Fatalf("console methods missing: %q", result.Console)
			}
		})
	}
}

func TestWebEvaluateNoAmbientAuthorityOrPrototypeEscape(t *testing.T) {
	engine := webEvaluateTestEngine(t)
	script := `function main() {
 const globals = ['window','document','fetch','WebSocket','XMLHttpRequest','process','require','Deno','Bun','std','os','readFile','writeFile','env','host','WebAssembly'];
 for (const key of globals) if (typeof globalThis[key] !== 'undefined') throw new Error('ambient capability: '+key);
 const g=({}).constructor.constructor('return globalThis')();
 if(g.fetch || g.process || g.require || g.window) throw new Error('constructor escape');
 Object.prototype.fetch = 'guest only'; globalThis.crossRequest = 1;
 return {safe:true};
}`
	if _, err := engine.Evaluate(context.Background(), []byte(script), []byte(`{}`), ""); err != nil {
		t.Fatal(err)
	}
	result, err := engine.Evaluate(context.Background(), []byte(`function main(){return {clean:globalThis.crossRequest===undefined && ({}).fetch===undefined}}`), []byte(`{}`), "")
	if err != nil || string(result.JSON) != `{"clean":true}` {
		t.Fatalf("cross-instance state: %s %v", result.JSON, err)
	}
	for _, script := range []string{
		`function main(){ return require('fs').readFileSync('/synthetic') }`,
		`function main(){ return fetch('/api/v1/meta',{credentials:'same-origin'}) }`,
		`function main(){ return new WebSocket('ws://synthetic.invalid') }`,
		`async function main(){ return await import('synthetic-module') }`,
		`import x from 'synthetic'; function main(){return x}`,
		`function main(){ return globalThis.process.env }`,
	} {
		if _, err := engine.Evaluate(context.Background(), []byte(script), []byte(`{}`), ""); err == nil {
			t.Fatalf("ambient access succeeded: %s", script)
		}
	}
}

func TestWebEvaluateBudgetsAndFailures(t *testing.T) {
	engine := webEvaluateTestEngine(t)
	cases := []struct{ name, script string }{
		{"syntax", `function main( {`},
		{"missing", `const missing=1`},
		{"reject", `async function main(){throw new Error('synthetic rejection')}`},
		{"error-output", `function main(){throw new Error('😀'.repeat(10000))}`},
		{"unsettled", `function main(){return new Promise(()=>{})}`},
		{"recursive", `function main(){return main()}`},
		{"allocation", `function main(){return {data:new Uint8Array(512*1024*1024)}}`},
		{"console", `function main(){console.log('x'.repeat(20000));return {}}`},
		{"output", `function main(){return {data:'x'.repeat(33*1024*1024)}}`},
		{"cycle", `function main(){const x={};x.x=x;return x}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_, err := engine.Evaluate(ctx, []byte(c.script), []byte(`{}`), "")
			var fault *webEvaluateFault
			if !errors.As(err, &fault) || len(fault.Message) > webEvaluateMaxError {
				t.Fatalf("unbounded or missing error: %v", err)
			}
		})
	}
	large := make([]byte, webEvaluateMaxBytes+1)
	if _, err := engine.Evaluate(context.Background(), large, []byte(`{}`), ""); err == nil {
		t.Fatal("oversized script admitted")
	}
}

func TestWebEvaluateCancellationAndBusyRelease(t *testing.T) {
	engine := webEvaluateTestEngine(t)
	for _, script := range []string{`while(true){}`, `function main(){while(true){}}`, `async function main(){while(true){await Promise.resolve()}}`} {
		ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
		started := time.Now()
		_, err := engine.Evaluate(ctx, []byte(script), []byte(`{}`), "")
		cancel()
		var fault *webEvaluateFault
		if !errors.As(err, &fault) || fault.Code != "evaluation_timeout" || time.Since(started) > 2*time.Second {
			t.Fatalf("loop not interrupted: %v, %s", err, time.Since(started))
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := engine.Evaluate(ctx, []byte(`function main(){return {}}`), []byte(`{}`), ""); err == nil {
		t.Fatal("cancelled request admitted")
	}
	a, err := webEvaluateAcquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	b, err := webEvaluateAcquire(context.Background())
	if err != nil {
		a()
		t.Fatal(err)
	}
	_, err = engine.Evaluate(context.Background(), nil, nil, "")
	a()
	b()
	var fault *webEvaluateFault
	if !errors.As(err, &fault) || fault.Code != "evaluation_busy" {
		t.Fatalf("third instance admitted: %v", err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Go(func() {
			result, err := engine.Evaluate(context.Background(), []byte(`let x=0;function main(){x++;return {x}}`), []byte(`{}`), "")
			if err != nil || string(result.JSON) != `{"x":1}` {
				t.Errorf("isolation: %s %v", result.JSON, err)
			}
		})
	}
	wg.Wait()
}

func TestWebEvaluateNativeProfileExtensionFixture(t *testing.T) {
	engine := webEvaluateTestEngine(t)
	script, err := os.ReadFile("../plugins/rust_api/rust/tests/fixtures/profile_script.js")
	if err != nil {
		t.Fatal(err)
	}
	input, err := os.ReadFile("../plugins/rust_api/rust/tests/fixtures/profile_config.json")
	if err != nil {
		t.Fatal(err)
	}
	result, err := engine.Evaluate(context.Background(), script, input, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err = json.Unmarshal(result.JSON, &config); err != nil {
		t.Fatal(err)
	}
	if len(config["proxies"].([]any)) != 10 || len(config["proxy-groups"].([]any)) != 17 || len(config["rules"].([]any)) != 12 {
		t.Fatal("native extension fixture changed shape")
	}
}
