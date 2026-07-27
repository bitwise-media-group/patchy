// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package transcriptstore

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/internal/transcript"
)

// DataKey holds the gzipped JSONL turns in the ConfigMap's binaryData.
// binaryData rather than data: agent output is arbitrary text, and gzip also
// buys roughly an order of magnitude of headroom under the 1 MiB object cap.
const DataKey = "turns.jsonl.gz"

// maxDecodedBytes bounds decompression on read, so a hand-edited object cannot
// balloon the reader's heap.
const maxDecodedBytes = 8 << 20

// NameFor returns the transcript ConfigMap name for a run child.
func NameFor(child string) string { return child + "-transcript" }

// Marshal renders turns as gzipped JSONL.
func Marshal(turns []transcript.Turn) ([]byte, error) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	enc := json.NewEncoder(zw)
	for _, t := range turns {
		t.V = transcript.Version
		if err := enc.Encode(t); err != nil {
			return nil, fmt.Errorf("transcriptstore: marshal: %w", err)
		}
	}
	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("transcriptstore: marshal: %w", err)
	}
	return buf.Bytes(), nil
}

// Unmarshal recovers turns from gzipped JSONL. Malformed trailing data ends
// the read without failing: a transcript is evidence, and a partial one is
// worth more than an error.
func Unmarshal(raw []byte) ([]transcript.Turn, error) {
	zr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("transcriptstore: unmarshal: %w", err)
	}
	defer func() { _ = zr.Close() }()

	var turns []transcript.Turn
	sc := bufio.NewScanner(io.LimitReader(zr, maxDecodedBytes))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var t transcript.Turn
		if json.Unmarshal(line, &t) != nil {
			break
		}
		turns = append(turns, t)
	}
	return turns, nil
}

// ConfigMap builds the transcript object for a run child.
func ConfigMap(namespace string, labels map[string]string, owner client.Object,
	gvk metav1.TypeMeta, turns []transcript.Turn) (*corev1.ConfigMap, error) {
	raw, err := Marshal(turns)
	if err != nil {
		return nil, err
	}
	yes := true
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      NameFor(owner.GetName()),
			Namespace: namespace,
			Labels:    labels,
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: gvk.APIVersion,
				Kind:       gvk.Kind,
				Name:       owner.GetName(),
				UID:        owner.GetUID(),
				Controller: &yes,
			}},
		},
		BinaryData: map[string][]byte{DataKey: raw},
	}, nil
}

// Persist writes a run's transcript and returns the reference to record on the
// run's status. No turns means no object and a nil reference — a harness that
// cannot transcribe, or a run that died before speaking, is not an error.
//
// The write is idempotent: a re-collected run overwrites its own transcript
// rather than failing on the object it already wrote.
func Persist(ctx context.Context, c client.Client, namespace string, labels map[string]string,
	owner client.Object, gvk metav1.TypeMeta, turns []transcript.Turn) (*v1alpha1.TranscriptRef, error) {
	if len(turns) == 0 {
		return nil, nil
	}
	cm, err := ConfigMap(namespace, labels, owner, gvk, turns)
	if err != nil {
		return nil, err
	}
	if err := c.Create(ctx, cm); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return nil, fmt.Errorf("transcriptstore: write %s: %w", cm.Name, err)
		}
		if err := c.Update(ctx, cm); err != nil {
			return nil, fmt.Errorf("transcriptstore: rewrite %s: %w", cm.Name, err)
		}
	}
	return &v1alpha1.TranscriptRef{
		Name:      cm.Name,
		Turns:     int32(len(turns)),
		Truncated: transcript.Truncated(turns),
	}, nil
}

// FromConfigMap reads the turns out of a transcript object. A missing key
// yields no turns and no error, so an object written by a different version of
// this package degrades to an empty conversation rather than an error.
func FromConfigMap(cm *corev1.ConfigMap) ([]transcript.Turn, error) {
	raw, ok := cm.BinaryData[DataKey]
	if !ok || len(raw) == 0 {
		return nil, nil
	}
	return Unmarshal(raw)
}

// Load reads a persisted transcript. A missing object yields no turns and no
// error: playback of an absent transcript is an empty conversation, not a
// failure.
func Load(ctx context.Context, r client.Reader, namespace, name string) ([]transcript.Turn, error) {
	var cm corev1.ConfigMap
	if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &cm); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("transcriptstore: read %s: %w", name, err)
	}
	return FromConfigMap(&cm)
}
