// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package mirror

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/crane"

	"github.com/bitwise-media-group/patchy/internal/mirror/spec"
)

// eventLog collects engine events; the engine emits from worker
// goroutines, so appends are locked.
type eventLog struct {
	mu     sync.Mutex
	events []Event
}

func (l *eventLog) add(ev Event) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, ev)
}

// warned reports whether any warning contains substr.
func (l *eventLog) warned(substr string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, ev := range l.events {
		if ev.Kind == "warn" && strings.Contains(ev.Message, substr) {
			return true
		}
	}
	return false
}

// loggedEngine builds the fixture engine with an event log attached.
func loggedEngine(f *fixture) (*Engine, *eventLog) {
	log := &eventLog{}
	f.eng = New(Config{Root: f.root, Global: f.global(), Now: func() time.Time { return testNow }, OnEvent: log.add})
	return f.eng, log
}

// lockedDemo writes the demo chart (pinned 1.0.0, rendering app:1.0.0),
// converges it, and returns the engine, its event log, the entry and the
// image digest the lock recorded.
func lockedDemo(t *testing.T, f *fixture) (*Engine, *eventLog, spec.Entry, string) {
	t.Helper()
	digest := f.pushImage(f.host+"/apps/app:1.0.0", testNow.AddDate(0, -1, 0))
	f.pushChart(f.host+"/upstream", "demo", "1.0.0", f.chartTgz("demo", "1.0.0", "1.0.0", appCanonical+":1.0.0"))
	f.pushChart(f.host+"/upstream", "demo", "1.1.0", f.chartTgz("demo", "1.1.0", "1.1.0", appCanonical+":1.1.0"))
	f.chartManifest("demo", "")
	f.write("charts/demo/values/discovery.yaml", "{}\n")
	eng, log := loggedEngine(f)
	entry, err := eng.Entry("demo")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Upgrade(context.Background(), entry, ""); err != nil {
		t.Fatalf("initial Upgrade: %v", err)
	}
	return eng, log, entry, digest
}

// lockedDigest reads the one image digest from the demo lock.
func lockedDigest(t *testing.T, entry spec.Entry) string {
	t.Helper()
	lock, err := spec.LoadImagesLock(entry.LockPath())
	if err != nil {
		t.Fatal(err)
	}
	if len(lock.Images) != 1 {
		t.Fatalf("lock images = %+v", lock.Images)
	}
	return lock.Images[0].Digest
}

// TestRegenerateKeepsLockedDigestWhenTagRebuilt pins the core rule: an
// upstream that re-pushes a released tag at a new digest moves nothing.
// validate stays green on the untouched tree, upgrade (both modes) keeps
// the locked digest, and the drift surfaces as a warning.
func TestRegenerateKeepsLockedDigestWhenTagRebuilt(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	eng, log, entry, digest := lockedDemo(t, f)

	rebuilt := f.pushImage(f.host+"/apps/app:1.0.0", testNow)
	if rebuilt == digest {
		t.Fatal("rebuild produced the same digest")
	}

	res, err := eng.Validate(ctx, entry, []string{StageRegen})
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if len(res.RegenDiffs) != 0 {
		t.Errorf("regen diffs = %v, want none: a rebuilt tag must not stale the lock", res.RegenDiffs)
	}
	if !log.warned("upstream rebuilt " + appCanonical + ":1.0.0 in place") {
		t.Errorf("no drift warning in %+v", log.events)
	}

	if _, err := eng.UpgradePinned(ctx, entry); err != nil {
		t.Fatalf("UpgradePinned: %v", err)
	}
	if got := lockedDigest(t, entry); got != digest {
		t.Errorf("UpgradePinned moved the lock to %s, want %s", got, digest)
	}
	upgraded, err := eng.Upgrade(ctx, entry, "")
	if err != nil {
		t.Fatalf("Upgrade: %v", err)
	}
	if len(upgraded.Changed) != 0 || lockedDigest(t, entry) != digest {
		t.Errorf("Upgrade changed %v, want a no-op keeping %s", upgraded.Changed, digest)
	}
}

// TestRegenerateResolvesNewTagsFresh: only a source new to the lock
// resolves its tag — a chart bump to an image tag the lock has never seen
// picks up that tag's current digest.
func TestRegenerateResolvesNewTagsFresh(t *testing.T) {
	f := newFixture(t)
	eng, _, entry, _ := lockedDemo(t, f)
	newDigest := f.pushImage(f.host+"/apps/app:1.1.0", testNow.AddDate(0, 0, -10))

	if _, err := eng.Upgrade(context.Background(), entry, "1.1.0"); err != nil {
		t.Fatalf("Upgrade: %v", err)
	}
	lock, err := spec.LoadImagesLock(entry.LockPath())
	if err != nil {
		t.Fatal(err)
	}
	if len(lock.Images) != 1 || lock.Images[0].Source != appCanonical+":1.1.0" || lock.Images[0].Digest != newDigest {
		t.Errorf("lock images = %+v, want %s:1.1.0@%s", lock.Images, appCanonical, newDigest)
	}
}

// TestRegenerateFailsWhenLockedDigestVanishes: a locked digest upstream
// no longer serves can't be kept honestly (verify, scan and sync all pull
// by it), so regeneration fails loudly instead of silently relocking.
func TestRegenerateFailsWhenLockedDigestVanishes(t *testing.T) {
	f := newFixture(t)
	eng, _, entry, digest := lockedDemo(t, f)

	f.pushImage(f.host+"/apps/app:1.0.0", testNow)
	if err := crane.Delete(f.host + "/apps/app@" + digest); err != nil {
		t.Fatalf("delete %s: %v", digest, err)
	}

	_, err := eng.Validate(context.Background(), entry, []string{StageRegen})
	if err == nil || !strings.Contains(err.Error(), "no longer pullable upstream") {
		t.Fatalf("Validate error = %v, want the locked digest reported unpullable", err)
	}
}

// TestRegenerateKeepsLockedArtifactDigest applies the same rule to
// kind: Artifact entries.
func TestRegenerateKeepsLockedArtifactDigest(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	canonical := "example.test/bundles/bundle"
	digest := f.pushImage(f.host+"/bundles/bundle:1.0.0", testNow.AddDate(0, -1, 0))
	f.artifactManifest(canonical, "")
	eng, log := loggedEngine(f)
	entry, err := eng.Entry("bundle")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Upgrade(ctx, entry, ""); err != nil {
		t.Fatalf("Upgrade: %v", err)
	}

	f.pushImage(f.host+"/bundles/bundle:1.0.0", testNow)
	res, err := eng.Validate(ctx, entry, []string{StageRegen})
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if len(res.RegenDiffs) != 0 {
		t.Errorf("regen diffs = %v, want none", res.RegenDiffs)
	}
	if !log.warned("in place") {
		t.Errorf("no drift warning in %+v", log.events)
	}
	lock, err := spec.LoadArtifactLock(entry.LockPath())
	if err != nil {
		t.Fatal(err)
	}
	if lock.Artifact.Digest != digest {
		t.Errorf("artifact lock digest = %s, want %s", lock.Artifact.Digest, digest)
	}
}
