// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package evalresults

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/bitwise-media-group/patchy/api/v1alpha1"
)

// DataKey holds the gzipped result entry in the ConfigMap's binaryData.
const DataKey = "entry.json.gz"

// maxDecodedBytes bounds decompression on read, so a hand-edited object
// cannot balloon the reader's heap.
const maxDecodedBytes = 8 << 20

// NameFor returns the results ConfigMap name for a unit.
func NameFor(unit string) string { return unit + "-results" }

// Persist writes a unit's result entry and returns the reference to record
// on the unit's status. An empty entry means no object and a nil reference —
// a unit that failed before producing a result is not an error here.
//
// The write is idempotent: a re-collected unit overwrites its own results
// object rather than failing on the one it already wrote.
func Persist(ctx context.Context, c client.Client, namespace string, labels map[string]string,
	owner client.Object, gvk metav1.TypeMeta, entry []byte) (*v1alpha1.TranscriptRef, error) {
	if len(entry) == 0 {
		return nil, nil
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(entry); err != nil {
		return nil, fmt.Errorf("evalresults: compress: %w", err)
	}
	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("evalresults: compress: %w", err)
	}

	yes := true
	cm := &corev1.ConfigMap{
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
		BinaryData: map[string][]byte{DataKey: buf.Bytes()},
	}
	if err := c.Create(ctx, cm); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return nil, fmt.Errorf("evalresults: write %s: %w", cm.Name, err)
		}
		if err := c.Update(ctx, cm); err != nil {
			return nil, fmt.Errorf("evalresults: rewrite %s: %w", cm.Name, err)
		}
	}
	return &v1alpha1.TranscriptRef{Name: cm.Name}, nil
}

// Load reads a persisted result entry. A missing object or key yields a nil
// entry and no error: the client treats an absent entry as a unit that
// produced no result.
func Load(ctx context.Context, r client.Reader, namespace, name string) ([]byte, error) {
	var cm corev1.ConfigMap
	if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &cm); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("evalresults: read %s: %w", name, err)
	}
	raw, ok := cm.BinaryData[DataKey]
	if !ok || len(raw) == 0 {
		return nil, nil
	}
	zr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("evalresults: decompress %s: %w", name, err)
	}
	defer func() { _ = zr.Close() }()
	entry, err := io.ReadAll(io.LimitReader(zr, maxDecodedBytes))
	if err != nil {
		return nil, fmt.Errorf("evalresults: decompress %s: %w", name, err)
	}
	return entry, nil
}
