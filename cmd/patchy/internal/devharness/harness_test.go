// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package devharness

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/bitwise-media-group/patchy/internal/generic"
	pkggeneric "github.com/bitwise-media-group/patchy/pkg/generic"
)

var testSecret = []byte("dev-secret")

// twoFindings is a valid delivery: one repo finding keyed by alertId, one
// cloud finding keyed by alertNumber only.
const twoFindings = `{"version":"v1","event":"findings","findings":[
  {"repo":{"owner":"acme","name":"orders"},"alertId":"a-1","ruleId":"sqli",
   "title":"SQL injection","description":"the description","severity":"high",
   "htmlUrl":"https://scanner.test/a-1"},
  {"cloudResource":{"provider":"google","name":"bucket-1","type":"storage.googleapis.com/Bucket"},
   "alertNumber":7,"title":"Public bucket","severity":"low"}
]}`

// running wires a Harness to a fake author endpoint and collects events.
type running struct {
	h      *Harness
	url    string // webhook URL from the listening event
	events *eventLog
	cancel context.CancelFunc
	done   chan error
}

// eventLog collects events; the harness's single worker emits sequentially,
// but the listening event races the test goroutine, so guard anyway.
type eventLog struct {
	ch chan Event
}

func (l *eventLog) emit(e Event) { l.ch <- e }

// next waits for the next event, failing the test on a stall.
func (l *eventLog) next(t *testing.T) Event {
	t.Helper()
	select {
	case e := <-l.ch:
		return e
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for a harness event")
		return Event{}
	}
}

// start runs a harness on an ephemeral port until the test ends.
func start(t *testing.T, cfg Config) *running {
	t.Helper()
	log := &eventLog{ch: make(chan Event, 64)}
	cfg.Addr = "127.0.0.1:0"
	cfg.Events = log.emit
	h, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	r := &running{h: h, events: log, cancel: cancel, done: make(chan error, 1)}
	go func() { r.done <- h.Run(ctx) }()
	t.Cleanup(func() { r.stop(t) })

	listening := log.next(t)
	if listening.Kind != "listening" || listening.WebhookURL == "" {
		t.Fatalf("first event = %+v, want a listening event with a URL", listening)
	}
	r.url = listening.WebhookURL
	return r
}

// stop cancels the harness and asserts a clean drain.
func (r *running) stop(t *testing.T) {
	t.Helper()
	r.cancel()
	select {
	case err := <-r.done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("Run() = %v, want nil or context.Canceled", err)
		}
	case <-time.After(15 * time.Second):
		t.Error("harness did not drain after cancellation")
	}
}

// post signs and delivers a payload, returning the HTTP status.
func post(t *testing.T, url string, secret []byte, deliveryID, payload string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader([]byte(payload)))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(pkggeneric.SignatureHeader, generic.Sign(secret, []byte(payload)))
	if deliveryID != "" {
		req.Header.Set(pkggeneric.DeliveryHeader, deliveryID)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("deliver: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode
}

// drain reads events until count events of kind have arrived, returning
// only those; interleaved events of other kinds are consumed and dropped.
func drain(t *testing.T, r *running, kind string, count int) []Event {
	t.Helper()
	return filter(drainAll(t, r, kind, count), kind)
}

// drainAll reads events until count events of kind have arrived, returning
// everything consumed on the way — for tests that assert on interleaved
// kinds without racing the pipeline.
func drainAll(t *testing.T, r *running, kind string, count int) []Event {
	t.Helper()
	var all []Event
	for seen := 0; seen < count; {
		e := r.events.next(t)
		all = append(all, e)
		if e.Kind == kind {
			seen++
		}
	}
	return all
}

// filter keeps the events of one kind.
func filter(events []Event, kind string) []Event {
	var out []Event
	for _, e := range events {
		if e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}

func TestFullPipeline(t *testing.T) {
	author := &authorProcess{secret: testSecret, enhanceReply: &pkggeneric.EnhanceResponse{
		Owners:          []string{"team-payments"},
		CommentMarkdown: "context from the warehouse",
		Attributes:      map[string]string{"tier": "1"},
	}}
	srv := author.serve()
	defer srv.Close()

	r := start(t, Config{
		Name: "warehouse", Secret: testSecret,
		EnhanceURL: srv.URL + "/enhance", ResolveURL: srv.URL + "/resolve",
		AutoResolve: true,
	})
	if !strings.HasSuffix(r.url, "/generic/warehouse/webhooks") {
		t.Fatalf("webhook URL = %s, want the /generic/warehouse/webhooks path", r.url)
	}
	if got := post(t, r.url, testSecret, "d-1", twoFindings); got != http.StatusAccepted {
		t.Fatalf("valid delivery = %d, want 202", got)
	}

	resolves := drain(t, r, "resolve", 2)
	for _, ev := range resolves {
		if ev.Err != "" {
			t.Errorf("resolve event failed: %s", ev.Err)
		}
	}
	enh, res := author.snapshot()
	assertEnhanceExchange(t, enh)
	assertResolveExchange(t, res)

	sum := r.h.Summary()
	want := Summary{Deliveries: 1, Findings: 2, EnhanceCalls: 2, ResolveCalls: 2}
	if sum != want {
		t.Errorf("Summary() = %+v, want %+v", sum, want)
	}
	if got := r.h.Findings(); len(got) != 2 || got[0].Source != "warehouse" {
		t.Errorf("Findings() = %+v, want 2 findings sourced warehouse", got)
	}
}

// assertEnhanceExchange checks the Issue mirrors what production sends for a
// fresh finding: no tracking-issue number, no labels — title, body, repo and
// cloudResource only.
func assertEnhanceExchange(t *testing.T, enh []pkggeneric.EnhanceRequest) {
	t.Helper()
	if len(enh) != 2 {
		t.Fatalf("enhancer received %d requests, want 2", len(enh))
	}
	first := enh[0]
	if first.Version != pkggeneric.Version || first.Integration != "warehouse" {
		t.Errorf("enhance envelope = %+v, want version v1 integration warehouse", first)
	}
	if first.Issue.Number != 0 || first.Issue.Labels != nil {
		t.Errorf("enhance issue carries Number/Labels (%+v); production never sets them", first.Issue)
	}
	if first.Issue.Title != "SQL injection" || first.Issue.Body != "the description" {
		t.Errorf("enhance issue = %+v, want title/body from the finding", first.Issue)
	}
	if first.Issue.Repo == nil || first.Issue.Repo.Owner != "acme" {
		t.Errorf("enhance issue repo = %+v, want acme/orders", first.Issue.Repo)
	}
	if enh[1].Issue.Repo != nil || enh[1].Issue.CloudResource == nil {
		t.Errorf("cloud finding's issue = %+v, want cloudResource and no repo", enh[1].Issue)
	}
}

// assertResolveExchange checks every write-back carries the canonical
// verdict and the toAlertRefs identity rule.
func assertResolveExchange(t *testing.T, res []pkggeneric.ResolveRequest) {
	t.Helper()
	if len(res) != 2 {
		t.Fatalf("resolver received %d requests, want 2", len(res))
	}
	wantVerdict := pkggeneric.Verdict{Kind: "ignore", Reason: "false positive",
		Comment: "Dismissed by patchy: investigation recommended ignore."}
	for i, rr := range res {
		if rr.Verdict != wantVerdict {
			t.Errorf("resolve %d verdict = %+v, want the canonical one", i, rr.Verdict)
		}
	}
	if res[0].Alerts[0].ID != "a-1" {
		t.Errorf("resolve 0 alert id = %q, want the delivered alertId", res[0].Alerts[0].ID)
	}
	if res[1].Alerts[0].ID != "7" {
		t.Errorf("resolve 1 alert id = %q, want the alert number as a string", res[1].Alerts[0].ID)
	}
}

func TestPingAnswers204AndIngestsNothing(t *testing.T) {
	r := start(t, Config{Name: "dev", Secret: testSecret})
	ping := `{"version":"v1","event":"ping"}`
	if got := post(t, r.url, testSecret, "", ping); got != http.StatusNoContent {
		t.Fatalf("ping = %d, want 204", got)
	}
	if sum := r.h.Summary(); sum.Deliveries != 0 || sum.Findings != 0 {
		t.Errorf("Summary() = %+v, want nothing processed", sum)
	}
}

func TestBadSignatureAndWrongName401(t *testing.T) {
	r := start(t, Config{Name: "dev", Secret: testSecret})
	if got := post(t, r.url, []byte("wrong-secret"), "", twoFindings); got != http.StatusUnauthorized {
		t.Errorf("bad signature = %d, want 401", got)
	}
	// A correct signature addressed to an unconfigured name fails closed the
	// same way: the path names the only credential that may validate it.
	other := strings.Replace(r.url, "/generic/dev/", "/generic/other/", 1)
	if got := post(t, other, testSecret, "", twoFindings); got != http.StatusUnauthorized {
		t.Errorf("unknown integration name = %d, want 401", got)
	}
	if sum := r.h.Summary(); sum.Deliveries != 0 {
		t.Errorf("Summary() = %+v, want no delivery recorded", sum)
	}
}

func TestInvalidFindingRejectsWholeDelivery(t *testing.T) {
	r := start(t, Config{Name: "dev", Secret: testSecret})
	// Second finding is invalid (no title): all-or-nothing validation must
	// keep the valid first one out too.
	payload := `{"version":"v1","event":"findings","findings":[
	  {"repo":{"owner":"a","name":"b"},"alertId":"ok-1","title":"valid","severity":"low"},
	  {"repo":{"owner":"a","name":"b"},"alertId":"bad-1","severity":"low"}
	]}`
	if got := post(t, r.url, testSecret, "", payload); got != http.StatusAccepted {
		t.Fatalf("delivery = %d, want 202 (validation is asynchronous)", got)
	}
	ev := drain(t, r, "error", 1)[0]
	if !strings.Contains(ev.Err, "title is required") {
		t.Errorf("error event = %q, want the validation failure", ev.Err)
	}
	if got := r.h.Findings(); len(got) != 0 {
		t.Errorf("Findings() = %+v, want none: validation is all-or-nothing", got)
	}
}

func TestMinSeverityDropsSilently(t *testing.T) {
	r := start(t, Config{Name: "dev", Secret: testSecret, MinSeverity: "high"})
	if got := post(t, r.url, testSecret, "", twoFindings); got != http.StatusAccepted {
		t.Fatalf("delivery = %d, want 202", got)
	}
	// Wait for the finding event, not just the delivery event: the worker
	// emits delivery before appending to the store, so reading Findings()
	// after only the delivery event races the append. The finding event is
	// emitted after it.
	all := drainAll(t, r, "finding", 1)
	ev := filter(all, "delivery")[0]
	if ev.Delivered != 2 || ev.Ingested != 1 {
		t.Errorf("delivery event = %+v, want 2 delivered, 1 ingested", ev)
	}
	if ev.Note == "" {
		t.Error("delivery event carries no note; a silent drop must be narrated")
	}
	findings := r.h.Findings()
	if len(findings) != 1 || findings[0].Severity != "high" {
		t.Errorf("Findings() = %+v, want only the high finding", findings)
	}
}

func TestDuplicateDeliveryDedups(t *testing.T) {
	r := start(t, Config{Name: "dev", Secret: testSecret})
	if got := post(t, r.url, testSecret, "same-id", twoFindings); got != http.StatusAccepted {
		t.Fatalf("first delivery = %d, want 202", got)
	}
	drain(t, r, "finding", 2)
	// Redelivery under the same id inside the dedup window is acknowledged
	// but not reprocessed — same as production.
	if got := post(t, r.url, testSecret, "same-id", twoFindings); got != http.StatusAccepted {
		t.Fatalf("redelivery = %d, want 202", got)
	}
	time.Sleep(100 * time.Millisecond) // give a would-be duplicate time to surface
	if sum := r.h.Summary(); sum.Deliveries != 1 || sum.Findings != 2 {
		t.Errorf("Summary() = %+v, want the redelivery deduped", sum)
	}
}

func TestEnhancerFailureStillResolves(t *testing.T) {
	author := &authorProcess{secret: testSecret, enhanceStatus: http.StatusInternalServerError}
	srv := author.serve()
	defer srv.Close()

	r := start(t, Config{
		Name: "dev", Secret: testSecret,
		EnhanceURL: srv.URL + "/enhance", ResolveURL: srv.URL + "/resolve",
		AutoResolve: true,
	})
	if got := post(t, r.url, testSecret, "", twoFindings); got != http.StatusAccepted {
		t.Fatalf("delivery = %d, want 202", got)
	}
	// Wait on the resolves: they come last, so both legs have finished.
	all := drainAll(t, r, "resolve", 2)
	for i, ev := range filter(all, "enhance") {
		if ev.Err == "" {
			t.Errorf("enhance %d succeeded; the author endpoint answered 500", i)
		}
	}
	_, res := author.snapshot()
	if len(res) != 2 {
		t.Errorf("resolver received %d requests, want 2: an enhancer failure never blocks resolve", len(res))
	}
	sum := r.h.Summary()
	if sum.EnhanceFailures != 2 || sum.ResolveFailures != 0 {
		t.Errorf("Summary() = %+v, want 2 enhance failures and clean resolves", sum)
	}
}

func TestEnhancer204IsNotAFailure(t *testing.T) {
	author := &authorProcess{secret: testSecret} // nil reply → 204
	srv := author.serve()
	defer srv.Close()

	r := start(t, Config{Name: "dev", Secret: testSecret, EnhanceURL: srv.URL + "/enhance"})
	if got := post(t, r.url, testSecret, "", twoFindings); got != http.StatusAccepted {
		t.Fatalf("delivery = %d, want 202", got)
	}
	for i, ev := range drain(t, r, "enhance", 2) {
		if ev.Err != "" {
			t.Errorf("enhance %d = error %q; 204 means nothing to contribute", i, ev.Err)
		}
		if ev.EnhanceResponse != nil {
			t.Errorf("enhance %d carries a response; 204 must read as nil", i)
		}
		if ev.Note == "" {
			t.Errorf("enhance %d has no note; the empty answer should be narrated", i)
		}
	}
	if sum := r.h.Summary(); sum.EnhanceFailures != 0 {
		t.Errorf("Summary() = %+v, want no enhance failures", sum)
	}
}

func TestNoAutoResolveRetains(t *testing.T) {
	author := &authorProcess{secret: testSecret}
	srv := author.serve()
	defer srv.Close()

	r := start(t, Config{
		Name: "dev", Secret: testSecret,
		ResolveURL: srv.URL + "/resolve", AutoResolve: false,
	})
	if got := post(t, r.url, testSecret, "", twoFindings); got != http.StatusAccepted {
		t.Fatalf("delivery = %d, want 202", got)
	}
	drain(t, r, "finding", 2)
	_, res := author.snapshot()
	if len(res) != 0 {
		t.Errorf("resolver received %d requests with auto-resolve off, want 0", len(res))
	}
	if got := r.h.Findings(); len(got) != 2 {
		t.Errorf("Findings() = %d, want both retained", len(got))
	}
}

func TestOversizedCommentIsNoted(t *testing.T) {
	author := &authorProcess{secret: testSecret, enhanceReply: &pkggeneric.EnhanceResponse{
		CommentMarkdown: strings.Repeat("x", maxEnrichmentMarkdown+1),
	}}
	srv := author.serve()
	defer srv.Close()

	r := start(t, Config{Name: "dev", Secret: testSecret, EnhanceURL: srv.URL + "/enhance"})
	if got := post(t, r.url, testSecret, "", twoFindings); got != http.StatusAccepted {
		t.Fatalf("delivery = %d, want 202", got)
	}
	ev := drain(t, r, "enhance", 1)[0]
	if !strings.Contains(ev.Note, "truncates") {
		t.Errorf("enhance note = %q, want the truncation warning", ev.Note)
	}
}

func TestParseFindings(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		want    int
		wantErr string
	}{
		{"valid", twoFindings, 2, ""},
		{"ping", `{"version":"v1","event":"ping"}`, 0, "no findings"},
		{"bad version", `{"version":"v2","event":"findings"}`, 0, "unsupported contract version"},
		{"invalid finding", `{"version":"v1","event":"findings","findings":[{"title":"x","severity":"low"}]}`, 0, "repo"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseFindings(context.Background(), "dev", []byte(tt.payload))
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("ParseFindings() error = %v, want one containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseFindings() error = %v", err)
			}
			if len(got) != tt.want {
				t.Errorf("ParseFindings() = %d findings, want %d", len(got), tt.want)
			}
		})
	}
}

func TestOneShots(t *testing.T) {
	author := &authorProcess{secret: testSecret, enhanceReply: &pkggeneric.EnhanceResponse{Owners: []string{"o"}}}
	srv := author.serve()
	defer srv.Close()

	findings, err := ParseFindings(context.Background(), "cmdb", []byte(twoFindings))
	if err != nil {
		t.Fatalf("ParseFindings: %v", err)
	}
	var events []Event
	emit := func(e Event) { events = append(events, e) }

	ec, err := NewClient(srv.URL+"/enhance", testSecret, 0)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := Enhance(context.Background(), ec, "cmdb", findings, emit); err != nil {
		t.Fatalf("Enhance: %v", err)
	}
	rc, err := NewClient(srv.URL+"/resolve", testSecret, 0)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := Resolve(context.Background(), rc, "cmdb", findings, emit); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	enh, res := author.snapshot()
	if len(enh) != 2 || len(res) != 2 {
		t.Fatalf("author saw %d enhance / %d resolve requests, want 2 / 2", len(enh), len(res))
	}
	if enh[0].Integration != "cmdb" || res[0].Integration != "cmdb" {
		t.Errorf("integration fields = %q / %q, want cmdb", enh[0].Integration, res[0].Integration)
	}
	if len(events) != 4 {
		t.Errorf("emitted %d events, want 4", len(events))
	}

	// A failing endpoint surfaces as a joined error, so the CLI exits non-zero.
	author.mu.Lock()
	author.resolveStatus = http.StatusConflict
	author.mu.Unlock()
	if err := Resolve(context.Background(), rc, "cmdb", findings, func(Event) {}); err == nil {
		t.Error("Resolve() = nil, want the endpoint failure")
	} else if !strings.Contains(err.Error(), fmt.Sprint(http.StatusConflict)) {
		t.Errorf("Resolve() error = %v, want the 409 surfaced", err)
	}
}
