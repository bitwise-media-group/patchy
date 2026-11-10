// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"go.yaml.in/yaml/v4"
)

// decodeLayer parses one config file into a generic map by its extension.
// An empty file is an empty layer.
func decodeLayer(ext string, raw []byte) (map[string]any, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return map[string]any{}, nil
	}
	var v any
	switch ext {
	case "yaml", "yml":
		dec := yaml.NewDecoder(bytes.NewReader(raw))
		if err := dec.Decode(&v); err != nil {
			if errors.Is(err, io.EOF) {
				return map[string]any{}, nil
			}
			return nil, err
		}
		var extra any
		if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
			return nil, errors.New("expected a single YAML document")
		}
	case "json":
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		if err := dec.Decode(&v); err != nil {
			return nil, err
		}
		if dec.More() {
			return nil, errors.New("expected a single JSON value")
		}
	case "toml":
		var m map[string]any
		if err := toml.Unmarshal(raw, &m); err != nil {
			return nil, err
		}
		v = m
	default:
		return nil, fmt.Errorf("unsupported config format %q", ext)
	}
	if v == nil {
		return map[string]any{}, nil
	}
	m, ok := normalize(v).(map[string]any)
	if !ok {
		return nil, errors.New("the top level must be a mapping")
	}
	return m, nil
}

// normalize converts the three decoders' outputs to one shape: string-keyed
// maps all the way down, and JSON numbers as int64 or float64 (as YAML and
// TOML already produce).
func normalize(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[k] = normalize(val)
		}
		return out
	case map[any]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[fmt.Sprint(k)] = normalize(val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = normalize(val)
		}
		return out
	case json.Number:
		if i, err := t.Int64(); err == nil && !strings.ContainsAny(t.String(), ".eE") {
			return i
		}
		if f, err := t.Float64(); err == nil {
			return f
		}
		return t.String()
	default:
		return v
	}
}
