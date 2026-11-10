// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package config

// Merge layers overlay onto base and returns the result, leaving both
// inputs untouched: maps merge recursively, lists and scalars in overlay
// replace base's wholesale, and an explicit nil (a YAML/JSON null) deletes
// the key — the way a project drops a value inherited from /etc or $HOME.
func Merge(base, overlay map[string]any) map[string]any {
	out := make(map[string]any, len(base)+len(overlay))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range overlay {
		if v == nil {
			delete(out, k)
			continue
		}
		if vm, ok := v.(map[string]any); ok {
			bm, _ := out[k].(map[string]any)
			// A fresh map still runs through Merge so nested nulls (deletes
			// of keys nothing set) never reach the typed decode.
			out[k] = Merge(bm, vm)
			continue
		}
		out[k] = v
	}
	return out
}
