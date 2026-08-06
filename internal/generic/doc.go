// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package generic implements the generic integration's behavior over the
// wire contract published in pkg/generic: the validating source handler that
// turns an inbound findings delivery into pkg/source Findings, and the
// signed outbound HTTP client behind both the verdict resolver and the
// context-enhancer call.
//
// Unlike every other source package there is no description template here:
// the external process authors its own markdown — patchy has no tool-native
// payload to render, only the normalized contract.
//
// A handler's source id is the Integration's NAME, not a package constant:
// any number of generic integrations coexist, and the name keeps their
// accumulation keys, labels, sticky comments, and write-back dispatch apart.
package generic
