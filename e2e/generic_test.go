// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package e2e

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/e2e/fakegithub"
	pkggeneric "github.com/bitwise-media-group/patchy/pkg/generic"
)

// The shared HMAC secrets of the two generic integrations this test seeds.
// The fixtures under fixtures/webhooks/generic.* are hand-built to the
// pkg/generic contract documented in docs/integrations/generic.md.
const (
	warehouseSecret = "e2e-warehouse-hmac-secret"
	cmdbSecret      = "e2e-cmdb-hmac-secret"
)

// externalProcess is the fake counterparty: one HTTP server standing in for
// every external process, recording each signed call patchy makes.
type externalProcess struct {
	mu       sync.Mutex
	secrets  map[string][]byte
	enhance  []pkggeneric.EnhanceRequest
	resolve  []pkggeneric.ResolveRequest
	badSigs  int
	enhanced func(req pkggeneric.EnhanceRequest) any
}

func (p *externalProcess) handler(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	var envelope struct {
		Integration string `json:"integration"`
	}
	_ = json.Unmarshal(body, &envelope)
	p.mu.Lock()
	defer p.mu.Unlock()
	secret := p.secrets[envelope.Integration]
	if !p.validSignature(secret, body, r.Header.Get("X-Patchy-Signature-256")) {
		p.badSigs++
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	switch r.URL.Path {
	case "/enhance":
		var req pkggeneric.EnhanceRequest
		if err := json.Unmarshal(body, &req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		p.enhance = append(p.enhance, req)
		resp := p.enhanced(req)
		if resp == nil {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	case "/resolve":
		var req pkggeneric.ResolveRequest
		if err := json.Unmarshal(body, &req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		p.resolve = append(p.resolve, req)
		w.WriteHeader(http.StatusOK)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (p *externalProcess) validSignature(secret, body []byte, header string) bool {
	if len(secret) == 0 || !strings.HasPrefix(header, "sha256=") {
		return false
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	want, err := hex.DecodeString(strings.TrimPrefix(header, "sha256="))
	if err != nil {
		return false
	}
	return hmac.Equal(mac.Sum(nil), want)
}

func (p *externalProcess) snapshot() (enhance []pkggeneric.EnhanceRequest, resolve []pkggeneric.ResolveRequest, badSigs int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]pkggeneric.EnhanceRequest(nil), p.enhance...),
		append([]pkggeneric.ResolveRequest(nil), p.resolve...),
		p.badSigs
}

// TestGenericWebhookPipeline drives the generic contract end to end: signed
// findings deliveries in through the wildcard route, the enhancer fan-out
// out to two integrations' endpoints, the tracking issue via the fallback
// tracker, and the verdict write-back on dismissal.
func TestGenericWebhookPipeline(t *testing.T) {
	cl := startCluster(t)

	gh := fakegithub.New()
	t.Cleanup(gh.Close)
	cl.githubCredentials(t, gh.URL)

	proc := &externalProcess{
		secrets: map[string][]byte{
			"warehouse": []byte(warehouseSecret),
			"cmdb":      []byte(cmdbSecret),
		},
		enhanced: func(req pkggeneric.EnhanceRequest) any {
			if req.Integration == "cmdb" {
				return map[string]any{
					"owners":          []string{"octocat"},
					"commentMarkdown": "cmdb: owned by team-warehouse",
					"attributes":      map[string]string{"system": "warehouse"},
				}
			}
			return nil // the warehouse has nothing to add about its own finding
		},
	}
	ext := httptest.NewServer(http.HandlerFunc(proc.handler))
	t.Cleanup(ext.Close)

	seedGenericIntegrations(t, cl, ext.URL)

	listen := fmt.Sprintf("127.0.0.1:%d", freePort(t))
	cl.controller(t, "integration-controller", "--listen-addr", listen)
	cl.controller(t, "context-controller")
	url := func(name string) string { return "http://" + listen + "/generic/" + name + "/webhooks" }

	findings := fixture(t, "generic.findings.json")

	// Authentication first: the route must fail closed before anything is
	// ingested. Note the last two cases — a real secret on the wrong
	// integration's path, and a real secret on a source-disabled
	// integration's path — both 401: candidates are strictly per-name.
	t.Run("rejects deliveries it cannot authenticate", func(t *testing.T) {
		for _, tt := range []struct{ name, path, secret string }{
			{"no signature at all", url("warehouse"), ""},
			{"the wrong secret", url("warehouse"), "not-the-secret"},
			{"an unknown integration", url("nobody"), warehouseSecret},
			{"another integration's secret", url("warehouse"), cmdbSecret},
			{"a source-disabled integration", url("cmdb"), cmdbSecret},
		} {
			t.Run(tt.name, func(t *testing.T) {
				if got := postGeneric(t, tt.path, tt.secret, "", findings); got != http.StatusUnauthorized {
					t.Errorf("status = %d, want 401", got)
				}
			})
		}
	})

	// The connectivity test answers 204 so an operator can wire the process
	// up before it has anything to say.
	t.Run("answers the ping with 204", func(t *testing.T) {
		ping := []byte(`{"version":"v1","event":"ping"}`)
		if got := postGeneric(t, url("warehouse"), warehouseSecret, "", ping); got != http.StatusNoContent {
			t.Errorf("ping = %d, want 204", got)
		}
	})

	// The real delivery.
	if got := postGeneric(t, url("warehouse"), warehouseSecret, "e2e-gen-1", findings); got != http.StatusAccepted {
		t.Fatalf("signed delivery = %d, want 202", got)
	}

	var fnd v1alpha1.Finding
	eventually(t, "the delivery to become a finding", func() bool {
		items := findingsBySource(t, cl, "warehouse")
		if len(items) != 1 {
			return false
		}
		fnd = items[0]
		return true
	})
	if fnd.Spec.Repository == nil || fnd.Spec.Repository.Name != "acme/orders" {
		t.Errorf("spec.repository = %+v, want the delivered repo", fnd.Spec.Repository)
	}
	if got := fnd.Spec.Advisories[0]; got != "CVE-2026-4242" {
		t.Errorf("advisories[0] = %q, want the delivered ordering kept", got)
	}
	if len(fnd.Spec.Alerts) != 1 || fnd.Spec.Alerts[0].ID != "e2e-wh-1001" {
		t.Errorf("alerts = %+v, want the delivered alert id", fnd.Spec.Alerts)
	}
	if got := fnd.Labels[v1alpha1.LabelSource]; got != "warehouse" {
		t.Errorf("source label = %q, want the integration name", got)
	}
	// The fallback tracker (M7): the warehouse integration has no issues
	// capability, so the tracking ref must point at the github Integration.
	if fnd.Spec.TrackingRef == nil || fnd.Spec.TrackingRef.Name != "github" {
		t.Errorf("trackingRef = %+v, want the github integration via fallback", fnd.Spec.TrackingRef)
	}

	// Enhancement fans out to both integrations' endpoints; only cmdb
	// contributes, and its enrichment is attributed to it.
	eventually(t, "the finding to be enhanced by the fan-out", func() bool {
		items := findingsBySource(t, cl, "warehouse")
		return len(items) == 1 && items[0].Status.Phase == v1alpha1.PhaseEnhanced
	})
	fnd = findingsBySource(t, cl, "warehouse")[0]
	if len(fnd.Status.Enrichments) != 1 || fnd.Status.Enrichments[0].Enhancer != "cmdb" {
		t.Fatalf("enrichments = %+v, want exactly cmdb's", fnd.Status.Enrichments)
	}
	if got := fnd.Status.Enrichments[0].Attributes["system"]; got != "warehouse" {
		t.Errorf("enrichment attributes = %v, want cmdb's contribution", fnd.Status.Enrichments[0].Attributes)
	}
	if len(fnd.Status.Owners) != 1 || fnd.Status.Owners[0] != "octocat" {
		t.Errorf("owners = %v, want cmdb's octocat", fnd.Status.Owners)
	}
	enhanceReqs, _, badSigs := proc.snapshot()
	if badSigs != 0 {
		t.Errorf("the external process rejected %d outbound signatures", badSigs)
	}
	seen := map[string]bool{}
	for _, req := range enhanceReqs {
		seen[req.Integration] = true
		if req.Issue.Title != "SQL injection in the nightly export" {
			t.Errorf("enhance request issue = %+v, want the finding's view", req.Issue)
		}
	}
	if !seen["warehouse"] || !seen["cmdb"] {
		t.Errorf("enhance requests reached %v, want both integrations", seen)
	}

	// The projection gives the generic finding a real tracking issue on the
	// fallback tracker.
	eventually(t, "the finding to get a tracking issue", func() bool {
		for _, is := range gh.Issues() {
			if strings.Contains(is.Title, "CVE-2026-4242") {
				return true
			}
		}
		return false
	})

	// A byte-identical redelivery with the same delivery id is absorbed.
	if got := postGeneric(t, url("warehouse"), warehouseSecret, "e2e-gen-1", findings); got != http.StatusAccepted {
		t.Fatalf("redelivery = %d, want 202", got)
	}
	consistently(t, "the redelivery not to open a second finding", func() bool {
		return len(findingsBySource(t, cl, "warehouse")) == 1
	})

	// A cloud finding travels too: no repo, cloudResource carried verbatim.
	if got := postGeneric(t, url("warehouse"), warehouseSecret, "e2e-gen-2",
		fixture(t, "generic.findings.cloud.json")); got != http.StatusAccepted {
		t.Fatalf("cloud delivery = %d, want 202", got)
	}
	eventually(t, "the cloud finding to arrive repo-less", func() bool {
		for _, f := range findingsBySource(t, cl, "warehouse") {
			if f.Spec.CloudResource != nil {
				return f.Spec.CloudResource.Provider == v1alpha1.CloudProviderGoogle &&
					f.Spec.Repository == nil
			}
		}
		return false
	})

	// Dismissal drives the verdict write-back to the warehouse's resolver.
	dismissFinding(t, cl, fnd.Name)
	eventually(t, "the verdict to reach the external process", func() bool {
		_, resolves, _ := proc.snapshot()
		return len(resolves) == 1
	})
	_, resolves, badSigs := proc.snapshot()
	if badSigs != 0 {
		t.Errorf("the external process rejected %d outbound signatures", badSigs)
	}
	rr := resolves[0]
	if rr.Integration != "warehouse" || rr.Verdict.Kind != "ignore" {
		t.Errorf("resolve request = %+v, want the warehouse's ignore verdict", rr)
	}
	if len(rr.Alerts) != 1 || rr.Alerts[0].ID != "e2e-wh-1001" {
		t.Errorf("resolved alerts = %+v, want the delivered alert", rr.Alerts)
	}
}

// seedGenericIntegrations creates two generic Integrations against the fake
// external process: warehouse (source + resolver + enhancer) and cmdb
// (enhancer only), each with its own HMAC secret.
func seedGenericIntegrations(t *testing.T, cl *cluster, extURL string) {
	t.Helper()
	ctx := t.Context()
	for name, secret := range map[string]string{"warehouse": warehouseSecret, "cmdb": cmdbSecret} {
		if err := cl.client.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "patchy-" + name, Namespace: namespace},
			StringData: map[string]string{"webhookSecret": secret},
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := cl.client.Create(ctx, &v1alpha1.Integration{
		ObjectMeta: metav1.ObjectMeta{Name: "warehouse", Namespace: namespace},
		Spec: v1alpha1.IntegrationSpec{
			Provider:  v1alpha1.IntegrationProviderGeneric,
			SecretRef: &v1alpha1.LocalSecretReference{Name: "patchy-warehouse"},
			Generic: &v1alpha1.GenericIntegration{
				Source: &v1alpha1.GenericSource{
					Enabled:  true,
					Resolver: &v1alpha1.GenericResolver{Enabled: true, URL: extURL + "/resolve"},
				},
				Enhance: &v1alpha1.GenericEnhancer{Enabled: true, URL: extURL + "/enhance"},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := cl.client.Create(ctx, &v1alpha1.Integration{
		ObjectMeta: metav1.ObjectMeta{Name: "cmdb", Namespace: namespace},
		Spec: v1alpha1.IntegrationSpec{
			Provider:  v1alpha1.IntegrationProviderGeneric,
			SecretRef: &v1alpha1.LocalSecretReference{Name: "patchy-cmdb"},
			Generic: &v1alpha1.GenericIntegration{
				Enhance: &v1alpha1.GenericEnhancer{Enabled: true, URL: extURL + "/enhance"},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
}

// dismissFinding forces the finding terminal, standing in for the
// investigation verdict the back half of the pipeline would produce.
func dismissFinding(t *testing.T, cl *cluster, name string) {
	t.Helper()
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var f v1alpha1.Finding
		if err := cl.client.Get(t.Context(), client.ObjectKey{Namespace: namespace, Name: name}, &f); err != nil {
			return err
		}
		f.Status.Phase = v1alpha1.PhaseDismissed
		return cl.client.Status().Update(t.Context(), &f)
	})
	if err != nil {
		t.Fatalf("dismiss finding: %v", err)
	}
}

// postGeneric signs and delivers one generic payload; an empty secret sends
// no signature header at all, and an empty delivery id lets the receiver
// derive one from the body.
func postGeneric(t *testing.T, url, secret, deliveryID string, payload []byte) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if secret != "" {
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(payload)
		req.Header.Set("X-Patchy-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	}
	if deliveryID != "" {
		req.Header.Set("X-Patchy-Delivery", deliveryID)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("deliver generic payload: %v", err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}
