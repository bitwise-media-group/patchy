// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package web

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/internal/archive/archivetest"
)

const archivePrefix = "patchy/finding-aa-1/finding-aa-1-rem-1/uid"

// archivedRemediation is a collected remediation whose record is archived.
func archivedRemediation() []client.Object {
	names := []string{"transcript.jsonl.gz", "report.md", "patch.mbox", "manifest.json"}
	objects := make([]v1alpha1.ArchiveObject, 0, len(names))
	for _, n := range names {
		objects = append(objects, v1alpha1.ArchiveObject{Name: n, Key: archivePrefix + "/" + n})
	}
	return []client.Object{&v1alpha1.Remediation{
		ObjectMeta: metav1.ObjectMeta{Name: "finding-aa-1-rem-1", Namespace: "patchy"},
		Spec:       v1alpha1.RemediationSpec{FindingRef: v1alpha1.ObjectReference{Name: "finding-aa-1"}, Attempt: 1},
		Status: v1alpha1.RemediationStatus{
			Phase: v1alpha1.RunComplete,
			Stage: &v1alpha1.StageResult{
				Outcome:    "ok",
				Transcript: &v1alpha1.TranscriptRef{Object: "transcript.jsonl.gz", Turns: 3},
				Archive: &v1alpha1.ArchiveRef{
					Provider: "s3", Bucket: "audit", Prefix: archivePrefix, Objects: objects,
				},
			},
		},
	}}
}

func archiveGet(t *testing.T, s *Server, path string) (*http.Response, []byte) {
	t.Helper()
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	res, err := http.Get(ts.URL + path)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	body, _ := io.ReadAll(res.Body)
	return res, body
}

func TestArchivePresigns(t *testing.T) {
	var logs bytes.Buffer
	s := transcriptServer(t, nil, archivedRemediation()...)
	s.log = slog.New(slog.NewTextHandler(&logs, nil))
	store := archivetest.New()
	store.Now = func() time.Time { return time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC) }
	s.WithArchive(store, 2*time.Minute)

	tests := []struct {
		object     string
		key        string
		attachment string
	}{
		{"transcript", archivePrefix + "/transcript.jsonl.gz", ""},
		{"patch", archivePrefix + "/patch.mbox", "finding-aa-1-rem-1-patch.mbox"},
		{"report", archivePrefix + "/report.md", "finding-aa-1-rem-1-report.md"},
		{"manifest", archivePrefix + "/manifest.json", ""},
	}
	for _, tt := range tests {
		t.Run(tt.object, func(t *testing.T) {
			res, body := archiveGet(t, s, "/api/findings/finding-aa-1/runs/remediation/1/archive/"+tt.object)
			if res.StatusCode != http.StatusOK {
				t.Fatalf("status = %d: %s", res.StatusCode, body)
			}
			var got PresignedURL
			if err := json.Unmarshal(body, &got); err != nil {
				t.Fatal(err)
			}
			u, _ := url.Parse(got.URL)
			if u.Path != "/"+tt.key || u.Query().Get("attachment") != tt.attachment {
				t.Errorf("url = %s, want key %s attachment %q", got.URL, tt.key, tt.attachment)
			}
			if got.ExpiresAt != "2026-11-20T12:02:00Z" {
				t.Errorf("expiresAt = %s, want the configured 2m ttl", got.ExpiresAt)
			}
		})
	}
	line := logs.String()
	for _, want := range []string{"archive object presigned", "user=" + operator.Username, "object=patch.mbox"} {
		if !strings.Contains(line, want) {
			t.Errorf("audit log lacks %q:\n%s", want, line)
		}
	}
}

func TestArchiveRefusals(t *testing.T) {
	tests := []struct {
		name       string
		objs       func(t *testing.T) []client.Object
		noSession  bool
		noGrant    bool
		noStore    bool
		path       string
		wantStatus int
	}{
		{name: "no session", objs: func(*testing.T) []client.Object { return archivedRemediation() },
			noSession: true, path: "remediation/1/archive/patch", wantStatus: http.StatusUnauthorized},
		{name: "no view grant", objs: func(*testing.T) []client.Object { return archivedRemediation() },
			noGrant: true, path: "remediation/1/archive/patch", wantStatus: http.StatusForbidden},
		{name: "unknown object", objs: func(*testing.T) []client.Object { return archivedRemediation() },
			path: "remediation/1/archive/secrets", wantStatus: http.StatusNotFound},
		{name: "not archived", objs: completedInvestigation,
			path: "investigation/1/archive/transcript", wantStatus: http.StatusNotFound},
		{name: "object not in the run", objs: func(*testing.T) []client.Object {
			objs := archivedRemediation()
			st := objs[0].(*v1alpha1.Remediation).Status.Stage
			st.Archive.Objects = st.Archive.Objects[:1]
			return objs
		}, path: "remediation/1/archive/patch", wantStatus: http.StatusNotFound},
		{name: "cross-finding", objs: func(*testing.T) []client.Object {
			objs := archivedRemediation()
			objs[0].(*v1alpha1.Remediation).Spec.FindingRef.Name = "someone-elses-finding"
			return objs
		}, path: "remediation/1/archive/patch", wantStatus: http.StatusNotFound},
		{name: "server without archive access", objs: func(*testing.T) []client.Object { return archivedRemediation() },
			noStore: true, path: "remediation/1/archive/patch", wantStatus: http.StatusNotFound},
		{name: "bad attempt", objs: func(*testing.T) []client.Object { return archivedRemediation() },
			path: "remediation/4294967297/archive/patch", wantStatus: http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := transcriptServer(t, nil, tt.objs(t)...)
			if tt.noSession {
				s.auth = stubAuth{}
			}
			if tt.noGrant {
				s.granter = stubGranter{}
			}
			if !tt.noStore {
				s.WithArchive(archivetest.New(), 0)
			}
			res, body := archiveGet(t, s, "/api/findings/finding-aa-1/runs/"+tt.path)
			if res.StatusCode != tt.wantStatus {
				t.Errorf("status = %d, want %d: %s", res.StatusCode, tt.wantStatus, body)
			}
		})
	}
}

func TestTranscriptOfArchivedRunSendsArchived(t *testing.T) {
	s := transcriptServer(t, nil, archivedRemediation()...)
	res, body := archiveGet(t, s, "/api/findings/finding-aa-1/runs/remediation/1/transcript")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", res.StatusCode)
	}
	text := string(body)
	if !strings.Contains(text, "event: archived\n") {
		t.Errorf("stream lacks the archived event:\n%s", text)
	}
	turns, ended := sseTurns(t, text)
	if len(turns) != 0 || !ended {
		t.Errorf("turns=%d ended=%v, want no turns and an end", len(turns), ended)
	}
}

func TestWireTranscriptArchived(t *testing.T) {
	got := wireTranscript(&v1alpha1.TranscriptRef{Object: "transcript.jsonl.gz", Turns: 4})
	if got == nil || !got.Archived {
		t.Errorf("wireTranscript(archived) = %+v", got)
	}
	if got = wireTranscript(&v1alpha1.TranscriptRef{Name: "cm", Turns: 4}); got == nil || got.Archived {
		t.Errorf("wireTranscript(configmap) = %+v", got)
	}
}
