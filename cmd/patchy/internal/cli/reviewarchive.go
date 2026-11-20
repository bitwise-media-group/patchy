// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package cli

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/cmd/patchy/internal/kubecfg"
	"github.com/bitwise-media-group/patchy/cmd/patchy/internal/render"
	"github.com/bitwise-media-group/patchy/internal/archive"
	"github.com/bitwise-media-group/patchy/internal/report"
	"github.com/bitwise-media-group/patchy/internal/transcript"
	"github.com/bitwise-media-group/patchy/internal/transcriptstore"
)

// cliPresignTTL is how long a URL from --print-url lives: long enough to
// paste into a browser or curl, short enough not to linger in shell history.
const cliPresignTTL = 15 * time.Minute

// maxArchivedLine bounds one archived transcript line. Archived turns are not
// compacted, so a single tool result can be large.
const maxArchivedLine = 32 << 20

// archived reports whether the review reads a run artifact rather than the
// report summary.
func (f *reviewFlags) archived() bool { return f.transcript || f.patch || f.reportFull }

// openArchive opens the store an archive reference names, with the caller's
// own cloud credentials. Tests substitute a fake.
func (o *Options) openArchive(ctx context.Context, ref *v1alpha1.ArchiveRef) (archive.Store, error) {
	cfg := archive.Config{
		Provider: archive.Provider(ref.Provider), Bucket: ref.Bucket, Region: ref.Region, Endpoint: ref.Endpoint,
	}
	if o.archiveFn != nil {
		return o.archiveFn(ctx, cfg)
	}
	return archive.Open(ctx, cfg)
}

// WithArchive pins the archive store the CLI reads, bypassing the cloud SDK
// credential chains.
func (o *Options) WithArchive(fn func(context.Context, archive.Config) (archive.Store, error)) {
	o.archiveFn = fn
}

// reviewArtifact prints (or presigns) one run artifact.
func reviewArtifact(ctx context.Context, opts *Options, env *kubecfg.Env, f *reviewFlags, obj client.Object) error {
	var st *v1alpha1.StageResult
	switch typed := obj.(type) {
	case *v1alpha1.Investigation:
		st = typed.Status.Stage
	case *v1alpha1.Remediation:
		st = typed.Status.Stage
	}
	if st == nil {
		return fmt.Errorf("%s has not finished, so it has nothing recorded yet", obj.GetName())
	}

	switch {
	case f.transcript:
		ref := st.Transcript
		if ref == nil {
			return fmt.Errorf("%s recorded no transcript", obj.GetName())
		}
		if ref.Object == "" {
			if f.printURL {
				return fmt.Errorf("%s's transcript is not archived, so there is no URL to print", obj.GetName())
			}
			turns, err := transcriptstore.Load(ctx, env.Client, obj.GetNamespace(), ref.Name)
			if err != nil {
				return err
			}
			if len(turns) == 0 {
				return fmt.Errorf("%s's transcript is gone (it expired with its finding)", obj.GetName())
			}
			return writeTurns(opts.Out, f.raw, turns)
		}
		return archivedObject(ctx, opts, f, obj.GetName(), st.Archive, ref.Object, func(body []byte) error {
			return writeArchivedTranscript(opts.Out, f.raw, body)
		})
	case f.patch:
		return archivedObject(ctx, opts, f, obj.GetName(), st.Archive, v1alpha1.ArchiveObjectPatch,
			func(body []byte) error {
				_, err := opts.Out.Write(body)
				return err
			})
	default:
		return archivedObject(ctx, opts, f, obj.GetName(), st.Archive, v1alpha1.ArchiveObjectReport,
			func(body []byte) error {
				text := string(body)
				if !f.raw {
					text = report.StripFrontmatter(text)
				}
				_, err := io.WriteString(opts.Out, text)
				return err
			})
	}
}

// archivedObject reads one archived object (or presigns it with
// --print-url) and hands its bytes to write.
func archivedObject(ctx context.Context, opts *Options, f *reviewFlags, run string,
	ref *v1alpha1.ArchiveRef, name string, write func([]byte) error,
) error {
	if ref == nil {
		return fmt.Errorf("%s was not archived (archival is off, or the upload failed)", run)
	}
	obj, ok := ref.Object(name)
	if !ok {
		return fmt.Errorf("%s's archive has no %s", run, name)
	}
	store, err := opts.openArchive(ctx, ref)
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()

	if f.printURL {
		url, _, err := store.Presign(ctx, obj.Key, cliPresignTTL, "")
		if err != nil {
			return err
		}
		notef(opts.Out, "%s\n", url)
		return nil
	}
	opts.debugf("reading %s", archive.Location{Provider: archive.Provider(ref.Provider), Bucket: ref.Bucket,
		Key: obj.Key}.URI())
	rc, err := store.Get(ctx, obj.Key)
	if err != nil {
		return err
	}
	defer func() { _ = rc.Close() }()
	body, err := io.ReadAll(rc)
	if err != nil {
		return fmt.Errorf("read %s: %w", obj.Key, err)
	}
	if obj.SHA256 != "" && archive.SHA256(body) != obj.SHA256 {
		return fmt.Errorf("%s does not match the digest the run recorded; refusing to print it", obj.Key)
	}
	return write(body)
}

// writeArchivedTranscript prints a gzipped JSONL transcript: verbatim JSONL
// with --raw, rendered text otherwise.
func writeArchivedTranscript(w io.Writer, raw bool, gz []byte) error {
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		return fmt.Errorf("transcript: %w", err)
	}
	defer func() { _ = zr.Close() }()
	if raw {
		_, err := io.Copy(w, zr)
		return err
	}
	var turns []transcript.Turn
	sc := bufio.NewScanner(zr)
	sc.Buffer(make([]byte, 64<<10), maxArchivedLine)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var t transcript.Turn
		if err := json.Unmarshal(line, &t); err != nil {
			return fmt.Errorf("transcript: %w", err)
		}
		turns = append(turns, t)
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("transcript: %w", err)
	}
	return render.Transcript(w, turns)
}

// writeTurns prints decoded turns: JSONL with --raw, rendered text otherwise.
func writeTurns(w io.Writer, raw bool, turns []transcript.Turn) error {
	if !raw {
		return render.Transcript(w, turns)
	}
	enc := json.NewEncoder(w)
	for _, t := range turns {
		t.V = transcript.Version
		if err := enc.Encode(t); err != nil {
			return err
		}
	}
	return nil
}
