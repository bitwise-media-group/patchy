// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package archive

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

// s3API is the slice of the S3 client the backend uses.
type s3API interface {
	PutObject(ctx context.Context, in *s3.PutObjectInput, opts ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	HeadObject(ctx context.Context, in *s3.HeadObjectInput, opts ...func(*s3.Options)) (*s3.HeadObjectOutput, error)
	GetObject(ctx context.Context, in *s3.GetObjectInput, opts ...func(*s3.Options)) (*s3.GetObjectOutput, error)
}

// s3Presigner is the slice of the presign client the backend uses.
type s3Presigner interface {
	PresignGetObject(ctx context.Context, in *s3.GetObjectInput,
		opts ...func(*s3.PresignOptions)) (*v4.PresignedHTTPRequest, error)
}

// s3Backend writes to one S3 bucket.
type s3Backend struct {
	api    s3API
	signer s3Presigner
	bucket string
	kmsKey string
}

// newS3 builds the backend from the SDK default chain (IRSA or EKS Pod
// Identity in-cluster, the caller's profile on a workstation).
func newS3(ctx context.Context, cfg Config) (*s3Backend, error) {
	var opts []func(*awsconfig.LoadOptions) error
	if cfg.Region != "" {
		opts = append(opts, awsconfig.WithRegion(cfg.Region))
	}
	sdk, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("archive: load aws credentials: %w", err)
	}
	return newS3FromConfig(sdk, cfg), nil
}

// newS3FromConfig builds the backend over an SDK config (tests pass static
// credentials and an httptest endpoint).
func newS3FromConfig(sdk aws.Config, cfg Config) *s3Backend {
	client := s3.NewFromConfig(sdk, func(o *s3.Options) {
		if cfg.Endpoint != "" {
			o.BaseEndpoint = aws.String(cfg.Endpoint)
			o.UsePathStyle = true
		}
	})
	return &s3Backend{
		api:    client,
		signer: s3.NewPresignClient(client),
		bucket: cfg.Bucket,
		kmsKey: cfg.KMSKey,
	}
}

func (b *s3Backend) create(ctx context.Context, key string, body []byte, contentType, sum string) error {
	raw, err := hex.DecodeString(sum)
	if err != nil {
		return fmt.Errorf("archive: sha256 %q: %w", sum, err)
	}
	in := &s3.PutObjectInput{
		Bucket:        aws.String(b.bucket),
		Key:           aws.String(key),
		Body:          bytes.NewReader(body),
		ContentLength: aws.Int64(int64(len(body))),
		ContentType:   aws.String(contentType),
		IfNoneMatch:   aws.String("*"),
		Metadata:      map[string]string{MetadataSHA256: sum},
		// S3 verifies the body against this before committing the object.
		ChecksumSHA256: aws.String(base64.StdEncoding.EncodeToString(raw)),
	}
	if b.kmsKey != "" {
		in.ServerSideEncryption = types.ServerSideEncryptionAwsKms
		in.SSEKMSKeyId = aws.String(b.kmsKey)
	}
	if _, err := b.api.PutObject(ctx, in); err != nil {
		if s3Status(err) == http.StatusPreconditionFailed {
			return errExists
		}
		return fmt.Errorf("archive: put s3://%s/%s: %w", b.bucket, key, err)
	}
	return nil
}

func (b *s3Backend) storedSHA256(ctx context.Context, key string) (string, error) {
	out, err := b.api.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(b.bucket), Key: aws.String(key)})
	if err != nil {
		if s3Status(err) == http.StatusNotFound {
			return "", ErrNotFound
		}
		return "", fmt.Errorf("archive: head s3://%s/%s: %w", b.bucket, key, err)
	}
	return out.Metadata[MetadataSHA256], nil
}

func (b *s3Backend) get(ctx context.Context, key string) (io.ReadCloser, error) {
	out, err := b.api.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(b.bucket), Key: aws.String(key)})
	if err != nil {
		if s3Status(err) == http.StatusNotFound {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("archive: get s3://%s/%s: %w", b.bucket, key, err)
	}
	return out.Body, nil
}

func (b *s3Backend) presign(ctx context.Context, key string, ttl time.Duration, attachment string) (string, error) {
	in := &s3.GetObjectInput{Bucket: aws.String(b.bucket), Key: aws.String(key)}
	if cd := contentDisposition(attachment); cd != "" {
		in.ResponseContentDisposition = aws.String(cd)
	}
	req, err := b.signer.PresignGetObject(ctx, in, s3.WithPresignExpires(ttl))
	if err != nil {
		return "", fmt.Errorf("archive: presign s3://%s/%s: %w", b.bucket, key, err)
	}
	return req.URL, nil
}

func (b *s3Backend) close() error { return nil }

// s3Status extracts the HTTP status from an SDK error, or 0. Status rather
// than error code, because S3-compatible stores disagree on codes (a 404 HEAD
// has no body to carry one) but not on statuses.
func s3Status(err error) int {
	var re *awshttp.ResponseError
	if errors.As(err, &re) {
		return re.HTTPStatusCode()
	}
	var ae smithy.APIError
	if errors.As(err, &ae) {
		switch ae.ErrorCode() {
		case "PreconditionFailed":
			return http.StatusPreconditionFailed
		case "NoSuchKey", "NotFound":
			return http.StatusNotFound
		}
	}
	return 0
}
