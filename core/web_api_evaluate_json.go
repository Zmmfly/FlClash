//go:build webpanel && !android

package core

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"unicode/utf8"
)

const webEvaluateMaxJSONNodes = 1000000
const webEvaluateMaxJSONDepth = 64

type webEvaluationJSONDecoder struct {
	ctx     context.Context
	decoder *json.Decoder
	nodes   int
}

func webEvaluationJSONError() error {
	return webEvaluateFailure("evaluation_invalid_output", "The script must produce a valid configuration object with unique JSON keys")
}

func (d *webEvaluationJSONDecoder) value(depth int) (any, error) {
	if err := d.ctx.Err(); err != nil {
		return nil, err
	}
	d.nodes++
	if depth > webEvaluateMaxJSONDepth || d.nodes > webEvaluateMaxJSONNodes {
		return nil, webEvaluateFailure("evaluation_structure_limit", "Evaluation JSON exceeds its structural budget")
	}
	token, err := d.decoder.Token()
	if err != nil {
		return nil, webEvaluationJSONError()
	}
	switch token := token.(type) {
	case json.Delim:
		switch token {
		case '{':
			object := map[string]any{}
			for d.decoder.More() {
				key, err := d.decoder.Token()
				if err != nil {
					return nil, webEvaluationJSONError()
				}
				name, ok := key.(string)
				if !ok {
					return nil, webEvaluationJSONError()
				}
				if _, duplicate := object[name]; duplicate {
					return nil, webEvaluationJSONError()
				}
				d.nodes++
				value, err := d.value(depth + 1)
				if err != nil {
					return nil, err
				}
				object[name] = value
			}
			end, err := d.decoder.Token()
			if err != nil || end != json.Delim('}') {
				return nil, webEvaluationJSONError()
			}
			return object, nil
		case '[':
			array := []any{}
			for d.decoder.More() {
				value, err := d.value(depth + 1)
				if err != nil {
					return nil, err
				}
				array = append(array, value)
			}
			end, err := d.decoder.Token()
			if err != nil || end != json.Delim(']') {
				return nil, webEvaluationJSONError()
			}
			return array, nil
		default:
			return nil, webEvaluationJSONError()
		}
	case json.Number:
		value, err := token.Float64()
		if err != nil {
			return nil, webEvaluationJSONError()
		}
		return value, nil
	default:
		return token, nil
	}
}

func webEvaluateDecodeJSON(ctx context.Context, data []byte) (map[string]any, error) {
	if len(data) > webEvaluateMaxBytes || !utf8.Valid(data) {
		return nil, webEvaluateFailure("evaluation_input_limit", "Evaluation JSON exceeds its UTF-8 byte budget")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	bounded := webEvaluationJSONDecoder{decoder: decoder, ctx: ctx}
	value, err := bounded.value(0)
	if err != nil {
		return nil, err
	}
	if _, err = decoder.Token(); err != io.EOF {
		return nil, webEvaluationJSONError()
	}
	root, ok := value.(map[string]any)
	if !ok {
		return nil, webEvaluationJSONError()
	}
	return root, nil
}
