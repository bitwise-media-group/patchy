// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package evaluation is the wire contract between patchy's evaluation
// execution service and the evolve client: the HTTP submission types, the
// in-pod event stream the runner emits on stdout, and the SSE monitor stream
// the API relays to the submitting client. It is stdlib-only so external
// processes can import it. Patchy interprets only the result and fatal
// events and treats everything else, including the finished results entry,
// as opaque payload.
//
// The split: patchy schedules sandboxed pods, injects the model credential,
// stores bounded summaries in CR statuses and the full-fidelity entry in a
// per-unit ConfigMap. Evolve owns every evaluation semantic (specs, grading,
// the LLM judge, baselines), co-located with the workspace bundle inside the
// pod.
package evaluation
