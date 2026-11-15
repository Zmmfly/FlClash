//go:build webpanel && !android

package core

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"sync"
)

//go:embed webui/login.html webui/intl_*.arb
var webLoginAssets embed.FS

var webLoginRenderOnce sync.Once
var webLoginPage []byte
var webLoginRenderError error

func renderWebLogin() ([]byte, error) {
	webLoginRenderOnce.Do(func() { webLoginPage, webLoginRenderError = renderWebLoginFS(webLoginAssets) })
	return webLoginPage, webLoginRenderError
}

func renderWebLoginFS(assets fs.FS) ([]byte, error) {
	template, err := fs.ReadFile(assets, "webui/login.html")
	if err != nil || bytes.Count(template, []byte("__FLCLASH_LOGIN_MESSAGES__")) != 1 {
		return nil, errors.New("invalid web login template")
	}
	messages := make(map[string]map[string]string)
	for _, locale := range []string{"en", "zh_CN", "ja", "ru", "fr", "es", "ko"} {
		data, err := fs.ReadFile(assets, "webui/intl_"+locale+".arb")
		if err != nil {
			return nil, errors.New("web login messages unavailable")
		}
		var translations map[string]string
		if json.Unmarshal(data, &translations) != nil || len(translations) == 0 {
			return nil, errors.New("invalid web login messages")
		}
		messages[locale] = translations
	}
	encoded, err := json.Marshal(messages)
	if err != nil {
		return nil, errors.New("cannot encode web login messages")
	}
	return bytes.Replace(template, []byte("__FLCLASH_LOGIN_MESSAGES__"), encoded, 1), nil
}
