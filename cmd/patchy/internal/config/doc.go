// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package config loads the layered patchy configuration the cluster-free
// command groups (dev, mirror, build) share: one file shape, merged from up
// to seven locations, least to most preferred —
//
//	/etc/patchy/config.<ext>
//	$HOME/.config/patchy/config.<ext>
//	<workdir>/mise/patchy.<ext>
//	<workdir>/patchy/config.<ext>
//	<workdir>/.mise/patchy.<ext>
//	<workdir>/.patchy/config.<ext>
//	<workdir>/patchy.<ext>
//
// where <ext> is yaml, yml, json or toml. Each layer decodes to a generic
// map; maps merge recursively, lists and scalars are replaced whole by the
// more specific layer, and an explicit null deletes an inherited key. The
// merge is deliberately not viper's: viper lowercases every key, which
// would corrupt user-defined keys like build args.
//
// Every layer is strictly decoded on its own first, so an unknown key is
// reported against the file that carries it; the merged map is then
// decoded once more into File. Semantic validation is per verb (File's
// MirrorConfig and BuildConfig accessors): a config with no registries is
// fine for the dev commands.
//
// The engines never see files: internal/mirror and internal/build receive
// the typed structs assembled here, which keeps mirror validate
// deterministic and the engines testable without a filesystem layout.
package config
