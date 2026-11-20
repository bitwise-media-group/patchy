// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package telemetry wires OpenTelemetry tracing, metrics, and structured
// logging into the patchy binaries and owns their lifecycle.
//
// Telemetry is off by default. Init picks one of three modes, with flags and
// config winning over the environment:
//
//   - File mode, when a directory is configured (Config.Dir, from each
//     binary's flags/config): one JSON file per signal (traces.json,
//     metrics.json, logs.json) via the OTEL stdout exporters. That is the
//     format otel-tui's --from-json-file reads, so a user can send us the
//     directory and we can replay it.
//   - Env mode, when no directory is set but OTEL_* environment variables are:
//     defers to contrib/exporters/autoexport (OTLP to a collector, console, or
//     none).
//   - Disabled otherwise: logging is the plain stderr handler.
//
// The package installs the global Tracer/Meter/Logger providers. Instrumented
// packages reach them through the otel.Tracer/otel.Meter globals and the
// default slog.Logger rather than importing this package, so telemetry never
// forms an import cycle with the code it observes. The slog logger fans every
// record out to the stderr text handler (gated at the configured verbosity)
// and to an otelslog bridge into the OTEL log pipeline (which accepts debug
// regardless). The exported file therefore captures full diagnostics while
// stderr stays at the chosen level. Logging goes to stderr only; stdout is
// reserved for agent-runner's event stream.
//
// Init returns a ShutdownFunc that flushes the providers in order
// (tracer, meter, logger) and then closes the files; main calls it inline after
// the command returns, since cobra skips PersistentPostRunE on error and os.Exit
// skips defers. Init never fails the caller: any setup error yields a working
// stderr-only logger plus a no-op shutdown alongside the error.
package telemetry
