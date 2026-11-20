# Run archival

Patchy can keep a complete, permanent record of every agent run in an Amazon S3 or Google Cloud Storage bucket. It is
off by default. Turn it on when your organisation needs an audit trail of exactly what an agent saw, said and changed,
kept for longer than patchy keeps the finding itself.

Without archival, everything an agent produces is temporary:

- the stored transcript is a summary (at most 500 turns of 2 KiB each, 512 KiB in total), kept in a ConfigMap that is
  deleted with its finding when the [finding TTL](../configuration/remediation-controller.md) expires (14 days by
  default);
- the report on the run's status is cut at 64 KiB;
- the remediation's change exists only as the pushed branch.

With archival on, each run attempt is written once to the bucket in full, and patchy never touches it again. How long
the record is kept, who may delete it, and which key encrypts it are all the bucket's policy, not patchy's.

## What is archived

Each run attempt gets one immutable prefix:

```text
<prefix>/<namespace>/<finding>/<run-name>/<run-uid>/
```

The run's UID is part of the path because finding and run names are reused after a finding expires and its alert comes
back. The prefix holds:

| Object                | Content                                                                                                                                                        |
| --------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `transcript.jsonl.gz` | Every turn the agent streamed, uncompacted, as gzipped JSONL                                                                                                   |
| `report.md`           | The full report the agent wrote, machine frontmatter included, untruncated                                                                                     |
| `patch.mbox`          | Remediations only: `git format-patch --binary` of the change, ready for `git am`                                                                               |
| `changeset.json.gz`   | Remediations only: the exact changeset patchy pushed to the forge                                                                                              |
| `manifest.json`       | Written **last**: the run, finding, alerts, repository and base SHA, harness, model, usage, outcome, timestamps, and the sha256 and size of every object above |
| `push.json`           | Remediations only, after a successful push: the branch, commit SHA and pull request                                                                            |

Because the manifest is written last, its presence means the set is complete, and its digests let anyone check that no
object was altered. `push.json` links the archive to the pull request it became.

Every object is stored with `Content-Type` only and no `Content-Encoding`, so neither store ever transcodes it: the
bytes you download are the bytes the manifest digests.

When a run is archived, its status carries `stage.archive` (the bucket, prefix and every object's key and digest) and
the `Archived` condition, and **no transcript ConfigMap is written**. The run still carries its final report, cut at 64
KiB as before. The status page and the CLI read the full record from the bucket.

!!! note "Known limit"

    The transcript is bounded by the agent pod's own stream cap: 64 KiB per turn and 8 MiB per run
    (`PATCHY_TRANSCRIPT_MAX_TURN_BYTES`, `PATCHY_TRANSCRIPT_MAX_TOTAL_BYTES`). The archive keeps everything the pod
    streamed, but nothing beyond it. A cut conversation says so on the turn that was cut.

## Writes and failures

All writes are create-only (S3 `If-None-Match: *`, GCS `DoesNotExist`), and each object's bytes depend only on the run's
output. A controller that retries a run rewrites identical bytes; the store sees an existing object with the same sha256
and treats the write as done. An existing object with a _different_ digest is an error and is never overwritten. Both
stores also verify each body in transit (S3 `x-amz-checksum-sha256`, GCS CRC32C).

The controller archives when it collects a finished Job, which is the only moment the agent's output is still readable.
A remediation is archived **before** it is pushed. If the upload fails, the controller changes nothing and tries again
with backoff, re-reading the pod log, for up to `--archive-retry-window` (default 45 minutes) after the Job finished.
That window must close before the Job's TTL removes the pod (`--job-ttl`, default 1 hour); the controllers refuse to
start otherwise.

What happens when the window closes depends on `--archive-required`:

| Setting                      | Effect                                                                                                                                                                                                                                                                                                                                              |
| ---------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `false` (default), fail open | The run carries on as if archival were off: the transcript summary goes to a ConfigMap, the run gets `Archived=False` (reason `ArchiveFailed`) and a Warning event, and `patchy_archive_failures_total{kind,provider}` counts it. The verdict is routed and the PR opened as usual.                                                                 |
| `true`, fail closed          | The run fails with outcome `archive_failed` and its finding goes straight to `Failed`. No verdict is routed and no pull request is opened, so nothing reaches the forge without a record. The transcript summary is still kept in a ConfigMap. A human [retry](../concepts/lifecycle.md#human-actions) relaunches the run once the bucket is fixed. |

`push.json` is best-effort in both modes: the pull request is already open by then, so a failure is logged and counted
(`patchy_archive_push_record_failures_total`) but changes nothing.

## Configuration

The same `PATCHY_ARCHIVE_*` settings go to the investigation-controller, the remediation-controller and the
status-server.

| Flag                     | Env                           | Default | Used by     | Purpose                                                                      |
| ------------------------ | ----------------------------- | ------- | ----------- | ---------------------------------------------------------------------------- |
| `--archive-provider`     | `PATCHY_ARCHIVE_PROVIDER`     | none    | all three   | `s3` or `gcs`; empty disables archival                                       |
| `--archive-bucket`       | `PATCHY_ARCHIVE_BUCKET`       | none    | all three   | The bucket                                                                   |
| `--archive-prefix`       | `PATCHY_ARCHIVE_PREFIX`       | none    | all three   | Key prefix every run is written under                                        |
| `--archive-region`       | `PATCHY_ARCHIVE_REGION`       | SDK     | all three   | S3 bucket region                                                             |
| `--archive-endpoint`     | `PATCHY_ARCHIVE_ENDPOINT`     | none    | all three   | Endpoint override for S3-compatible stores (path-style)                      |
| `--archive-kms-key`      | `PATCHY_ARCHIVE_KMS_KEY`      | bucket  | controllers | KMS key per write: an S3 key id or ARN, or a GCS CryptoKey name              |
| `--archive-required`     | `PATCHY_ARCHIVE_REQUIRED`     | `false` | controllers | Fail runs that cannot be archived instead of falling back                    |
| `--archive-retry-window` | `PATCHY_ARCHIVE_RETRY_WINDOW` | `45m`   | controllers | How long after a Job finishes a failed upload is retried (below `--job-ttl`) |
| `--archive-presign-ttl`  | `PATCHY_ARCHIVE_PRESIGN_TTL`  | `5m`    | status      | How long a presigned URL handed to the browser stays valid (at most 7 days)  |

With Helm, set the `archive:` block instead; the chart renders these keys into each component's ConfigMap and adds the
NetworkPolicy egress the credential chain needs:

```yaml
archive:
  enabled: true
  provider: s3
  bucket: acme-patchy-audit
  prefix: prod
  region: eu-west-2
  kmsKey: arn:aws:kms:eu-west-2:111122223333:key/1234abcd-...
  required: true
  retryWindow: 45m
investigationController:
  serviceAccount:
    annotations:
      eks.amazonaws.com/role-arn: arn:aws:iam::111122223333:role/patchy-archive-writer
remediationController:
  serviceAccount:
    annotations:
      eks.amazonaws.com/role-arn: arn:aws:iam::111122223333:role/patchy-archive-writer
statusServer:
  serviceAccount:
    annotations:
      eks.amazonaws.com/role-arn: arn:aws:iam::111122223333:role/patchy-archive-reader
```

With kustomize, uncomment the `PATCHY_ARCHIVE_*` keys in your overlay's ConfigMap patch (an example is in
`deploy/kustomize/overlays/prod/configmap-patch.yaml`). If you use EKS Pod Identity or GKE workload identity, also add
an egress rule to the node-local metadata endpoint: `169.254.170.23:80` for EKS Pod Identity, or `169.254.169.254:80`
and `169.254.169.252:988` for GKE. IRSA needs no extra rule, because it exchanges its token with STS over 443, which the
base policies already allow; so is the bucket itself.

## Identity and permissions

Credentials always come from the platform's default chain (IRSA or EKS Pod Identity, GKE workload identity, or ADC). No
key material exists anywhere in patchy.

| Who                                       | S3                                                                                                                   | GCS                                                                                                                                                                        |
| ----------------------------------------- | -------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| investigation- and remediation-controller | `s3:PutObject`, `s3:GetObject` (the idempotent-retry check), plus `kms:GenerateDataKey` and `kms:Decrypt` on the key | `roles/storage.objectCreator` and `storage.objects.get`, plus `cloudkms.cryptoKeyEncrypterDecrypter` on the key                                                            |
| status-server                             | `s3:GetObject`, plus `kms:Decrypt`                                                                                   | `storage.objects.get`, `cloudkms.cryptoKeyDecrypter`, and `iam.serviceAccounts.signBlob` on **its own** service account (`roles/iam.serviceAccountTokenCreator` on itself) |
| CLI users                                 | `s3:GetObject`, plus `kms:Decrypt`                                                                                   | `storage.objects.get`, plus `cloudkms.cryptoKeyDecrypter`                                                                                                                  |

No role needs delete or overwrite. With Object Lock or a locked retention policy, nobody can delete the objects before
they expire anyway.

GCS signs URLs with the service account's key. On workload identity there is no key file, so the client library signs
through the IAM `signBlob` API instead, which is why the status server needs `signBlob` on itself. A presigned S3 URL
made from temporary credentials (IRSA, Pod Identity) stops working when those credentials expire, even if its own expiry
is later; at the default 5 minutes this never matters.

## The status page and the CLI

The status server never streams bucket bytes. When you open an archived run's conversation, the transcript stream
answers `archived`; the page asks the server for a presigned URL
(`GET /api/findings/{name}/runs/{kind}/{attempt}/archive/{object}`, with the same `get findings` grant and finding check
as the transcript), fetches the gzipped JSONL straight from the bucket, and decompresses it in the browser. "Download
patch" and "Full report" work the same way. Every presign is logged with the user, run and object.

Because the browser fetches from the bucket directly, **the bucket needs a CORS rule** allowing `GET` from the status
page's origin. If your status page sets a Content-Security-Policy, add the bucket's origin
(`https://<bucket>.s3.<region>.amazonaws.com` or `https://storage.googleapis.com`) to `connect-src`.

The CLI reads the bucket with **your own** cloud credentials (the AWS default chain or Google ADC), using only what the
run's status records, so it needs no patchy configuration:

```sh
patchy review investigation --finding my-finding --transcript | less
patchy review remediation --finding my-finding --patch | git am
patchy review remediation --finding my-finding --report-full
patchy review remediation --finding my-finding --patch --print-url   # a 15-minute presigned URL
```

The CLI checks each object against the digest recorded on the run before printing it.

## Terraform examples

These are starting points; adapt names, retention and key policy to your organisation.

### S3

```hcl
resource "aws_kms_key" "patchy_archive" {
  description         = "patchy run archive"
  enable_key_rotation = true
}

resource "aws_s3_bucket" "patchy_archive" {
  bucket              = "acme-patchy-audit"
  object_lock_enabled = true
}

resource "aws_s3_bucket_versioning" "patchy_archive" {
  bucket = aws_s3_bucket.patchy_archive.id
  versioning_configuration {
    status = "Enabled"
  }
}

resource "aws_s3_bucket_object_lock_configuration" "patchy_archive" {
  bucket = aws_s3_bucket.patchy_archive.id
  rule {
    default_retention {
      mode  = "COMPLIANCE"
      years = 7
    }
  }
}

resource "aws_s3_bucket_server_side_encryption_configuration" "patchy_archive" {
  bucket = aws_s3_bucket.patchy_archive.id
  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm     = "aws:kms"
      kms_master_key_id = aws_kms_key.patchy_archive.arn
    }
    bucket_key_enabled = true
  }
}

resource "aws_s3_bucket_public_access_block" "patchy_archive" {
  bucket                  = aws_s3_bucket.patchy_archive.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_lifecycle_configuration" "patchy_archive" {
  bucket = aws_s3_bucket.patchy_archive.id
  rule {
    id     = "tier-then-expire"
    status = "Enabled"
    filter {}
    transition {
      days          = 0
      storage_class = "INTELLIGENT_TIERING"
    }
    expiration {
      days = 2557 # just past the 7-year lock
    }
    noncurrent_version_expiration {
      noncurrent_days = 2557
    }
  }
}

resource "aws_s3_bucket_cors_configuration" "patchy_archive" {
  bucket = aws_s3_bucket.patchy_archive.id
  cors_rule {
    allowed_methods = ["GET"]
    allowed_origins = ["https://patchy-status.example.com"]
    max_age_seconds = 3000
  }
}

data "aws_iam_policy_document" "patchy_archive_writer" {
  statement {
    actions   = ["s3:PutObject", "s3:GetObject"]
    resources = ["${aws_s3_bucket.patchy_archive.arn}/*"]
  }
  statement {
    actions   = ["kms:GenerateDataKey", "kms:Decrypt"]
    resources = [aws_kms_key.patchy_archive.arn]
  }
}

data "aws_iam_policy_document" "patchy_archive_reader" {
  statement {
    actions   = ["s3:GetObject"]
    resources = ["${aws_s3_bucket.patchy_archive.arn}/*"]
  }
  statement {
    actions   = ["kms:Decrypt"]
    resources = [aws_kms_key.patchy_archive.arn]
  }
}
```

Attach the writer policy to the role the investigation and remediation controllers assume, and the reader policy to the
status server's role and to the humans who use the CLI.

### GCS

```hcl
data "google_storage_project_service_account" "gcs" {}

resource "google_kms_key_ring" "patchy" {
  name     = "patchy"
  location = "europe-west2"
}

resource "google_kms_crypto_key" "patchy_archive" {
  name            = "patchy-archive"
  key_ring        = google_kms_key_ring.patchy.id
  rotation_period = "7776000s"
}

resource "google_storage_bucket" "patchy_archive" {
  name                        = "acme-patchy-audit"
  location                    = "EUROPE-WEST2"
  uniform_bucket_level_access = true
  public_access_prevention    = "enforced"

  encryption {
    default_kms_key_name = google_kms_crypto_key.patchy_archive.id
  }

  retention_policy {
    retention_period = 220898664 # 7 years, in seconds
    is_locked        = true      # irreversible: test with false first
  }

  autoclass {
    enabled = true
  }

  lifecycle_rule {
    condition {
      age = 2557
    }
    action {
      type = "Delete"
    }
  }

  cors {
    origin          = ["https://patchy-status.example.com"]
    method          = ["GET"]
    max_age_seconds = 3000
  }
}

# The Cloud Storage service agent encrypts with the key.
resource "google_kms_crypto_key_iam_member" "patchy_archive_gcs" {
  crypto_key_id = google_kms_crypto_key.patchy_archive.id
  role          = "roles/cloudkms.cryptoKeyEncrypterDecrypter"
  member        = "serviceAccount:${data.google_storage_project_service_account.gcs.email_address}"
}

resource "google_storage_bucket_iam_member" "writer_create" {
  bucket = google_storage_bucket.patchy_archive.name
  role   = "roles/storage.objectCreator"
  member = "serviceAccount:${google_service_account.patchy_archive_writer.email}"
}

resource "google_storage_bucket_iam_member" "writer_read" {
  bucket = google_storage_bucket.patchy_archive.name
  role   = "roles/storage.objectViewer"
  member = "serviceAccount:${google_service_account.patchy_archive_writer.email}"
}

resource "google_storage_bucket_iam_member" "status_read" {
  bucket = google_storage_bucket.patchy_archive.name
  role   = "roles/storage.objectViewer"
  member = "serviceAccount:${google_service_account.patchy_status.email}"
}

# Presigning on workload identity signs through IAM signBlob.
resource "google_service_account_iam_member" "status_sign" {
  service_account_id = google_service_account.patchy_status.name
  role               = "roles/iam.serviceAccountTokenCreator"
  member             = "serviceAccount:${google_service_account.patchy_status.email}"
}
```

Bind each Google service account to its Kubernetes ServiceAccount with workload identity
(`iam.gke.io/gcp-service-account` annotation plus a `roles/iam.workloadIdentityUser` binding).
