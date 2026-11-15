//go:build webpanel && !android

package core

import (
	"context"
	"encoding/json"
	"github.com/metacubex/mihomo/config"
	"reflect"
	"strings"
)

var webEvaluateRawFields = func() map[string]string {
	fields := map[string]string{}
	typeOf := reflect.TypeFor[config.RawConfig]()
	for i := 0; i < typeOf.NumField(); i++ {
		field := typeOf.Field(i)
		yamlName := strings.Split(field.Tag.Get("yaml"), ",")[0]
		jsonName := strings.Split(field.Tag.Get("json"), ",")[0]
		if yamlName != "" && yamlName != "-" && jsonName != "" && jsonName != "-" {
			fields[yamlName] = jsonName
		}
	}
	return fields
}()

func webEvaluateValidate(ctx context.Context, data []byte) (json.RawMessage, WebAPIEvaluationCounts, error) {
	var counts WebAPIEvaluationCounts
	root, err := webEvaluateDecodeJSON(ctx, data)
	if err != nil {
		return nil, counts, err
	}
	if err = webRuntimePathPolicy(root, ""); err != nil {
		return nil, counts, err
	}
	projection := map[string]any{}
	for yamlName, jsonName := range webEvaluateRawFields {
		if value, ok := root[yamlName]; ok {
			projection[jsonName] = value
		}
	}
	typedJSON, err := json.Marshal(projection)
	if err != nil {
		return nil, counts, webEvaluationJSONError()
	}
	raw := config.DefaultRawConfig()
	if err = json.Unmarshal(typedJSON, raw); err != nil {
		return nil, counts, webEvaluationJSONError()
	}
	if err = ctx.Err(); err != nil {
		return nil, counts, err
	}
	if err = ValidateProxyDependencies(raw); err != nil {
		return nil, counts, err
	}
	if err = ctx.Err(); err != nil {
		return nil, counts, err
	}
	canonical, err := json.Marshal(root)
	if err != nil || len(canonical) > webEvaluateMaxBytes {
		return nil, counts, webAPIError(413, "evaluation_output_limit", "The canonical result exceeds its byte budget")
	}
	counts = WebAPIEvaluationCounts{Proxies: len(raw.Proxy), Groups: len(raw.ProxyGroup), Rules: len(raw.Rule)}
	return canonical, counts, nil
}
