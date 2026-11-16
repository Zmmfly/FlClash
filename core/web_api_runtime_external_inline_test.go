//go:build webpanel && !android

package core

import (
	"context"
	"testing"

	"github.com/metacubex/mihomo/adapter/provider"
	C "github.com/metacubex/mihomo/constant"
	cp "github.com/metacubex/mihomo/constant/provider"
	"github.com/metacubex/mihomo/tunnel"
)

func TestWebRuntimeExternalProvidersSkipInlineProviders(t *testing.T) {
	inline, err := provider.NewInlineProvider("Sub", nil, func([]byte) ([]C.Proxy, error) { return nil, nil }, provider.NewHealthCheck(nil, "", 0, 0, false, nil))
	if err != nil {
		t.Fatal(err)
	}
	proxies, providers := tunnel.Proxies(), tunnel.Providers()
	t.Cleanup(func() { tunnel.UpdateProxies(proxies, providers) })
	tunnel.UpdateProxies(proxies, map[string]cp.ProxyProvider{"Sub": inline})
	listed, err := webRuntimeNativeExternalProviders(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 0 {
		t.Fatal(listed)
	}
}
