// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package tokenauth

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	authnv1 "k8s.io/api/authentication/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

const (
	testHeader   = "X-Test-Token"
	testAudience = "test-audience"
)

var testSubject = ServiceAccount("patchy-agents", "patchy-agent")

// fakeReviews wires a fake clientset whose TokenReview verdict is driven by
// token value: "good" authenticates as testSubject (pod agent-pod-1),
// "other" as a foreign identity, "down" fails the review call, and anything
// else fails authentication. It records the audiences each review asked for.
func fakeReviews(calls *atomic.Int64, audiences *[]string) *fake.Clientset {
	cs := fake.NewClientset()
	cs.PrependReactor("create", "tokenreviews", func(action k8stesting.Action) (bool, runtime.Object, error) {
		calls.Add(1)
		tr := action.(k8stesting.CreateAction).GetObject().(*authnv1.TokenReview)
		if audiences != nil {
			*audiences = append(*audiences, tr.Spec.Audiences...)
		}
		out := tr.DeepCopy()
		switch tr.Spec.Token {
		case "good":
			out.Status = authnv1.TokenReviewStatus{
				Authenticated: true,
				User: authnv1.UserInfo{
					Username: testSubject,
					Extra:    map[string]authnv1.ExtraValue{PodNameExtraKey: {"agent-pod-1"}},
				},
			}
		case "other":
			out.Status = authnv1.TokenReviewStatus{
				Authenticated: true,
				User:          authnv1.UserInfo{Username: "system:serviceaccount:other:sa"},
			}
		case "down":
			return true, nil, errors.New("api server unavailable")
		default:
			out.Status = authnv1.TokenReviewStatus{Authenticated: false}
		}
		return true, out, nil
	})
	return cs
}

func request(token string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	if token != "" {
		r.Header.Set(testHeader, token)
	}
	return r
}

func TestAuthenticate(t *testing.T) {
	tests := []struct {
		name    string
		token   string
		wantErr string
		wantPod string
	}{
		{name: "no token", token: "", wantErr: "missing " + testHeader},
		{name: "bad token", token: "expired", wantErr: "not authenticated for audience " + testAudience},
		{name: "foreign identity", token: "other", wantErr: "not an accepted service account"},
		{name: "review unavailable", token: "down", wantErr: "token review unavailable"},
		{name: "accepted identity", token: "good", wantPod: "agent-pod-1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls atomic.Int64
			var audiences []string
			a := New(fakeReviews(&calls, &audiences), Config{Audience: testAudience, Subjects: []string{testSubject}})
			id, err := a.Authenticate(request(tt.token), testHeader)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
				}
				if strings.Contains(err.Error(), tt.token) && tt.token != "" {
					t.Fatalf("error echoes the token: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if id.User != testSubject || id.Pod != tt.wantPod {
				t.Fatalf("identity = %+v", id)
			}
			if len(audiences) != 1 || audiences[0] != testAudience {
				t.Fatalf("reviewed audiences = %v, want [%s]", audiences, testAudience)
			}
		})
	}
}

func TestVerdictCache(t *testing.T) {
	var calls atomic.Int64
	a := New(fakeReviews(&calls, nil), Config{Audience: testAudience, Subjects: []string{testSubject}})
	now := time.Unix(1_000_000, 0)
	a.now = func() time.Time { return now }

	for range 3 {
		if _, err := a.Authenticate(request("good"), testHeader); err != nil {
			t.Fatal(err)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("token reviews = %d, want 1 (verdict cached)", got)
	}

	// Denials are cached too: retrying a bad token must not add QPS.
	for range 3 {
		if _, err := a.Authenticate(request("expired"), testHeader); err == nil {
			t.Fatal("bad token accepted")
		}
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("token reviews = %d, want 2 (denial cached)", got)
	}

	// An unavailable review is not a verdict: the next call asks again.
	for range 2 {
		_, _ = a.Authenticate(request("down"), testHeader)
		now = now.Add(time.Millisecond)
	}
	if got := calls.Load(); got != 4 {
		t.Fatalf("token reviews = %d, want 4 (outage not cached)", got)
	}

	// Past the TTL the verdict is reviewed again.
	now = now.Add(DefaultTTL + time.Second)
	if _, err := a.Authenticate(request("good"), testHeader); err != nil {
		t.Fatal(err)
	}
	if got := calls.Load(); got != 5 {
		t.Fatalf("token reviews = %d, want 5 (expired verdict re-reviewed)", got)
	}
}

func TestMultipleSubjects(t *testing.T) {
	var calls atomic.Int64
	a := New(fakeReviews(&calls, nil), Config{
		Audience: testAudience,
		Subjects: []string{"system:serviceaccount:other:sa", testSubject},
	})
	for _, tok := range []string{"good", "other"} {
		if _, err := a.Authenticate(request(tok), testHeader); err != nil {
			t.Fatalf("%s: %v", tok, err)
		}
	}
}
