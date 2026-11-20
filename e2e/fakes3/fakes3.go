// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package fakes3 is a minimal S3 endpoint standing in for AWS at the network
// edge, the way fakegithub stands in for GitHub and fakeoidc for Google.
//
// It speaks just enough of the path-style object API for patchy's run
// archive (PUT, GET, HEAD) and, crucially, checks what the real service
// checks: every request must carry a valid SigV4 signature for the one
// access key it knows, either in the Authorization header (the SDK client)
// or in the query string (a presigned URL, which must also be unexpired),
// and a PUT with If-None-Match: * fails with 412 when the key exists. So the
// archive's create-only writes, idempotent retries and presigned URLs are
// exercised against the real signing code rather than a stub that accepts
// anything.
package fakes3

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
)

// The one credential the fake accepts. Callers export these to the process
// under test as AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY / AWS_REGION.
const (
	AccessKeyID     = "AKIAFAKES3E2E"
	SecretAccessKey = "fakes3-e2e-secret"
	Region          = "eu-west-2"
)

// Object is one stored object.
type Object struct {
	Body        []byte
	ContentType string
	Metadata    map[string]string
}

// Request records one request the fake answered.
type Request struct {
	Method    string
	Key       string
	Presigned bool
	Status    int
	Header    http.Header
}

// Server is a running fake S3.
type Server struct {
	*httptest.Server

	mu       sync.Mutex
	objects  map[string]Object
	requests []Request
}

// Start brings up the fake. Its URL is the endpoint to configure (path-style
// addressing: /<bucket>/<key>).
func Start() *Server {
	s := &Server{objects: map[string]Object{}}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	return s
}

// Object returns a stored object by bucket and key.
func (s *Server) Object(bucket, key string) (Object, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.objects[bucket+"/"+key]
	return o, ok
}

// Keys lists the stored keys of a bucket.
func (s *Server) Keys(bucket string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for k := range s.objects {
		if rest, ok := strings.CutPrefix(k, bucket+"/"); ok {
			out = append(out, rest)
		}
	}
	return out
}

// Requests returns every request answered so far.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.requests...)
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	presigned := r.URL.Query().Get("X-Amz-Signature") != ""
	status := s.handle(w, r, body, presigned)
	s.mu.Lock()
	s.requests = append(s.requests, Request{
		Method: r.Method, Key: strings.TrimPrefix(r.URL.Path, "/"), Presigned: presigned,
		Status: status, Header: r.Header.Clone(),
	})
	s.mu.Unlock()
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request, body []byte, presigned bool) int {
	var authErr error
	if presigned {
		authErr = verifyPresigned(r)
	} else {
		authErr = verifyHeader(r, body)
	}
	if authErr != nil {
		return writeError(w, http.StatusForbidden, "SignatureDoesNotMatch", authErr.Error())
	}

	path := strings.TrimPrefix(r.URL.Path, "/")
	if !strings.Contains(path, "/") {
		return writeError(w, http.StatusBadRequest, "InvalidRequest", "path-style /<bucket>/<key> only")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	switch r.Method {
	case http.MethodPut:
		if presigned {
			return writeError(w, http.StatusForbidden, "AccessDenied", "presigned writes are not issued")
		}
		if _, exists := s.objects[path]; exists && r.Header.Get("If-None-Match") == "*" {
			return writeError(w, http.StatusPreconditionFailed, "PreconditionFailed",
				"At least one of the pre-conditions you specified did not hold")
		}
		meta := map[string]string{}
		for k, v := range r.Header {
			if name, ok := strings.CutPrefix(strings.ToLower(k), "x-amz-meta-"); ok {
				meta[name] = v[0]
			}
		}
		s.objects[path] = Object{Body: body, ContentType: r.Header.Get("Content-Type"), Metadata: meta}
		w.WriteHeader(http.StatusOK)
		return http.StatusOK
	case http.MethodGet, http.MethodHead:
		o, ok := s.objects[path]
		if !ok {
			if r.Method == http.MethodHead {
				w.WriteHeader(http.StatusNotFound)
				return http.StatusNotFound
			}
			return writeError(w, http.StatusNotFound, "NoSuchKey", "The specified key does not exist.")
		}
		h := w.Header()
		h.Set("Content-Type", o.ContentType)
		h.Set("Content-Length", strconv.Itoa(len(o.Body)))
		for k, v := range o.Metadata {
			h.Set("X-Amz-Meta-"+k, v)
		}
		if cd := r.URL.Query().Get("response-content-disposition"); cd != "" {
			h.Set("Content-Disposition", cd)
		}
		w.WriteHeader(http.StatusOK)
		if r.Method == http.MethodGet {
			_, _ = w.Write(o.Body)
		}
		return http.StatusOK
	default:
		return writeError(w, http.StatusMethodNotAllowed, "MethodNotAllowed", r.Method)
	}
}

func writeError(w http.ResponseWriter, status int, code, msg string) int {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, "<Error><Code>%s</Code><Message>%s</Message></Error>", code, msg)
	return status
}

var creds = aws.Credentials{AccessKeyID: AccessKeyID, SecretAccessKey: SecretAccessKey}

// verifyHeader re-signs a header-authenticated request with the SDK's own
// signer and compares signatures, after checking the body against the
// payload hash the client signed.
func verifyHeader(r *http.Request, body []byte) error {
	auth := r.Header.Get("Authorization")
	fields, ok := strings.CutPrefix(auth, "AWS4-HMAC-SHA256 ")
	if !ok {
		return errors.New("missing SigV4 Authorization header")
	}
	parts := map[string]string{}
	for f := range strings.SplitSeq(fields, ",") {
		k, v, _ := strings.Cut(strings.TrimSpace(f), "=")
		parts[k] = v
	}
	if err := checkCredential(parts["Credential"]); err != nil {
		return err
	}
	payload := r.Header.Get("X-Amz-Content-Sha256")
	if payload != "UNSIGNED-PAYLOAD" {
		sum := sha256.Sum256(body)
		if payload != hex.EncodeToString(sum[:]) {
			return errors.New("body does not match x-amz-content-sha256")
		}
	}
	at, err := time.Parse("20060102T150405Z", r.Header.Get("X-Amz-Date"))
	if err != nil {
		return fmt.Errorf("x-amz-date: %w", err)
	}

	// Rebuild the request carrying exactly the headers the client signed.
	re, err := http.NewRequest(r.Method, "http://"+r.Host+r.URL.RequestURI(), bytes.NewReader(body))
	if err != nil {
		return err
	}
	re.Header = http.Header{}
	for h := range strings.SplitSeq(parts["SignedHeaders"], ";") {
		switch h {
		case "host":
		case "content-length":
			re.ContentLength = r.ContentLength
		default:
			re.Header[http.CanonicalHeaderKey(h)] = r.Header.Values(h)
		}
	}
	if err := v4.NewSigner().SignHTTP(context.Background(), creds, re, payload, "s3", Region, at,
		func(o *v4.SignerOptions) { o.DisableURIPathEscaping = true }); err != nil {
		return err
	}
	if signature(re.Header.Get("Authorization")) != parts["Signature"] {
		return errors.New("signature mismatch")
	}
	return nil
}

// verifyPresigned re-presigns the URL's request with the SDK's own signer and
// compares signatures, then checks the URL has not expired.
func verifyPresigned(r *http.Request) error {
	q := r.URL.Query()
	if err := checkCredential(q.Get("X-Amz-Credential")); err != nil {
		return err
	}
	at, err := time.Parse("20060102T150405Z", q.Get("X-Amz-Date"))
	if err != nil {
		return fmt.Errorf("X-Amz-Date: %w", err)
	}
	expires, err := strconv.Atoi(q.Get("X-Amz-Expires"))
	if err != nil {
		return fmt.Errorf("X-Amz-Expires: %w", err)
	}
	if time.Now().After(at.Add(time.Duration(expires) * time.Second)) {
		return errors.New("request has expired")
	}
	want := q.Get("X-Amz-Signature")
	for _, k := range []string{"X-Amz-Algorithm", "X-Amz-Credential", "X-Amz-Date", "X-Amz-SignedHeaders",
		"X-Amz-Signature", "X-Amz-Security-Token"} {
		q.Del(k)
	}
	u := *r.URL
	u.Scheme, u.Host, u.RawQuery = "http", r.Host, q.Encode()
	re, err := http.NewRequest(r.Method, u.String(), nil)
	if err != nil {
		return err
	}
	signed, _, err := v4.NewSigner().PresignHTTP(context.Background(), creds, re, "UNSIGNED-PAYLOAD", "s3",
		Region, at, func(o *v4.SignerOptions) { o.DisableURIPathEscaping = true })
	if err != nil {
		return err
	}
	su, err := url.Parse(signed)
	if err != nil {
		return err
	}
	if su.Query().Get("X-Amz-Signature") != want {
		return errors.New("presigned signature mismatch")
	}
	return nil
}

// checkCredential accepts only the fake's own key, region and service.
func checkCredential(cred string) error {
	p := strings.Split(cred, "/")
	if len(p) != 5 || p[0] != AccessKeyID || p[2] != Region || p[3] != "s3" || p[4] != "aws4_request" {
		return fmt.Errorf("unknown credential scope %q", cred)
	}
	return nil
}

// signature extracts Signature= from an Authorization header value.
func signature(auth string) string {
	_, sig, _ := strings.Cut(auth, "Signature=")
	return sig
}
