// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package harness

import (
	"os"

	"github.com/bitwise-media-group/patchy/internal/runner"
)

// FakeFixtureEnv names the environment variable pointing at the stream-json
// fixture file the Fake harness replays.
const FakeFixtureEnv = "PATCHY_FAKE_FIXTURE"

// Fake is a stand-in harness for tests and the agent-runner's --fake mode: it
// replays a captured stream-json fixture through cat instead of invoking an
// agent, and parses it with the same stream-json helpers as Claude, so
// everything downstream of PromptSpec is exercised for real. Unix-only (it
// shells out to /bin/cat's argv convention); the real deployments it fakes
// run in Linux pods anyway.
type Fake struct {
	base
}

// NewFake returns the builtin fake harness.
func NewFake() *Fake {
	return &Fake{base: base{
		id:   "fake",
		name: "Fake",
		clis: []string{"cat"},
		// No credentials: the fixture replay needs none.
		envKeys: nil,
	}}
}

// PromptSpec ignores the request's prompt and flags and replays the fixture
// named by FakeFixtureEnv; ws and Env still land on the spec so runner-side
// behavior (workspace, environment) stays faithful.
func (f *Fake) PromptSpec(ws string, req PromptRequest) runner.CommandSpec {
	return runner.CommandSpec{
		Argv: []string{"cat", os.Getenv(FakeFixtureEnv)},
		Dir:  ws,
		Env:  req.Env,
	}
}

// ParseResult parses the fixture exactly as Claude parses live output.
func (f *Fake) ParseResult(stdout []byte) (AgentResult, bool) {
	return parseStreamResult(stdout)
}

// RuntimeError classifies the fixture exactly as Claude classifies live output.
func (f *Fake) RuntimeError(stdout []byte, exitCode int, timedOut bool) string {
	return streamRuntimeError(stdout, exitCode, timedOut)
}

// ScanUsage scans fixture lines exactly as Claude scans live ones.
func (f *Fake) ScanUsage(line []byte) (int, bool) {
	return scanStreamUsage(line)
}
