// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package stats

import (
	"context"
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// scopeName identifies this package's meter.
const scopeName = "github.com/bitwise-media-group/patchy/internal/stats"

// The instruments fire at rollup time — exactly once per run/finding,
// aligned with the CR counters. Anyone with an OTLP backend gets the same
// numbers the FindingRollup objects carry; the CRs remain the durable
// record. `repo` is the only high-cardinality attribute (~estate size);
// constrained backends can drop it with a metric view.
var (
	stageRuns = sync.OnceValue(func() metric.Int64Counter {
		c, _ := otel.Meter(scopeName).Int64Counter("patchy.stage.runs",
			metric.WithDescription("agent runs by stage/harness/model/outcome"))
		return c
	})
	stageTokens = sync.OnceValue(func() metric.Int64Counter {
		c, _ := otel.Meter(scopeName).Int64Counter("patchy.stage.tokens",
			metric.WithDescription("agent tokens by stage and class"))
		return c
	})
	stageCost = sync.OnceValue(func() metric.Float64Counter {
		c, _ := otel.Meter(scopeName).Float64Counter("patchy.stage.cost",
			metric.WithDescription("agent spend"), metric.WithUnit("usd"))
		return c
	})
	findingCompleted = sync.OnceValue(func() metric.Int64Counter {
		c, _ := otel.Meter(scopeName).Int64Counter("patchy.finding.completed",
			metric.WithDescription("findings reaching a terminal phase"))
		return c
	})
	findingDeleted = sync.OnceValue(func() metric.Int64Counter {
		c, _ := otel.Meter(scopeName).Int64Counter("patchy.finding.deleted",
			metric.WithDescription("finding resources deleted"))
		return c
	})
)

// RecordStage emits one run's metrics (called at total-scope aggregation so
// it fires exactly once per run).
func RecordStage(ctx context.Context, d StageDelta, repo string) {
	attrs := metric.WithAttributes(
		attribute.String("stage", d.Stage),
		attribute.String("harness", d.Harness),
		attribute.String("model", d.Model),
		attribute.String("outcome", d.Outcome),
		attribute.String("repo", repo),
	)
	stageRuns().Add(ctx, 1, attrs)
	stageCost().Add(ctx, float64(d.CostMicroUSD)/1e6, attrs)
	for class, n := range map[string]int64{
		"input":          d.InputTokens,
		"output":         d.OutputTokens,
		"cache_read":     d.CacheReadTokens,
		"cache_creation": d.CacheCreationTokens,
	} {
		if n > 0 {
			stageTokens().Add(ctx, n, metric.WithAttributes(
				attribute.String("stage", d.Stage),
				attribute.String("class", class)))
		}
	}
}

// RecordCompletion emits a finding's terminal-entry metrics (first rollup
// only).
func RecordCompletion(ctx context.Context, phase, recommendation, repo string) {
	findingCompleted().Add(ctx, 1, metric.WithAttributes(
		attribute.String("phase", phase),
		attribute.String("recommendation", recommendation),
		attribute.String("repo", repo)))
}

// RecordDeleted emits a finding deletion (reason: ttl or manual).
func RecordDeleted(ctx context.Context, reason string) {
	findingDeleted().Add(ctx, 1, metric.WithAttributes(attribute.String("reason", reason)))
}
