// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package ghas

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/bitwise-media-group/patchy/internal/ghclient"
	"github.com/bitwise-media-group/patchy/internal/templates"
	"github.com/bitwise-media-group/patchy/pkg/source"
)

// SecretsID is the secret-scanning source identifier.
const SecretsID = "ghas-secrets"

// SecretsEventType is the webhook event the secret-scanning source consumes.
const SecretsEventType = "secret_scanning_alert"

// secretsActionable are the secret alert actions that produce a finding.
// resolved, validated, publicly_leaked and the rest are state changes on an
// alert patchy already holds, or noise.
var secretsActionable = map[string]bool{
	"created":  true,
	"reopened": true,
}

// maxDescribedLocations bounds the locations the description lists; the
// alert link covers the rest.
const maxDescribedLocations = 20

// SecretAlertGetter fetches the full secret-scanning alert. The webhook
// payload carries the secret in plain text, so the source never reads the
// alert from the payload: it re-fetches it with the secret hidden.
type SecretAlertGetter interface {
	GetSecretAlert(ctx context.Context, repo ghclient.Repo, number int) (*ghclient.SecretAlert, error)
}

// SecretAlertResolver closes a secret-scanning alert on the forge.
type SecretAlertResolver interface {
	ResolveSecretAlert(ctx context.Context, repo ghclient.Repo, number int, resolution, comment string) error
}

// SecretAlertEnumerator walks the open secret-scanning alerts the
// credential can reach (ghclient.AppSecretAlertEnumerator or
// PATSecretAlertEnumerator), with AlertEnumerator's contract.
type SecretAlertEnumerator interface {
	Enumerate(
		ctx context.Context, repos []string, yield func(ghclient.Repo, *ghclient.SecretAlert) bool,
	) (complete bool, err error)
}

// Secrets is the GitHub secret-scanning source plugin. Like Handler, each
// constructor builds one role: ingest, write-back, or backfill listing.
type Secrets struct {
	alerts  SecretAlertGetter
	resolve SecretAlertResolver
	repo    ghclient.Repo
	enum    SecretAlertEnumerator
}

// Secrets implements every source interface.
var (
	_ source.Handler  = (*Secrets)(nil)
	_ source.Resolver = (*Secrets)(nil)
	_ source.Lister   = (*Secrets)(nil)
)

// NewSecrets builds the ingest handler around a secret-alert fetcher.
func NewSecrets(alerts SecretAlertGetter) *Secrets { return &Secrets{alerts: alerts} }

// NewSecretsResolver builds the write-back handler, resolving alerts on
// repo. A zero repo leaves nothing on GitHub to resolve.
func NewSecretsResolver(r SecretAlertResolver, repo ghclient.Repo) *Secrets {
	return &Secrets{resolve: r, repo: repo}
}

// NewSecretsLister builds the backfill handler over e.
func NewSecretsLister(e SecretAlertEnumerator) *Secrets { return &Secrets{enum: e} }

// ID implements source.Handler.
func (s *Secrets) ID() string { return SecretsID }

// Findings implements source.Handler. The delivery is decoded only for the
// action, alert number and repository; the alert itself (minus its secret)
// comes from the API.
func (s *Secrets) Findings(ctx context.Context, event string, payload []byte) ([]source.Finding, error) {
	if event != SecretsEventType {
		return nil, nil
	}
	var d delivery
	if err := json.Unmarshal(payload, &d); err != nil {
		return nil, fmt.Errorf("ghas: decode %s payload: %w", SecretsEventType, err)
	}
	if !secretsActionable[d.Action] {
		return nil, nil
	}
	repo := ghclient.Repo{Owner: d.Repository.Owner.Login, Name: d.Repository.Name}
	if repo.Owner == "" || repo.Name == "" || d.Alert.Number == 0 {
		return nil, fmt.Errorf("ghas: %s payload missing repository or alert number", SecretsEventType)
	}
	alert, err := s.alerts.GetSecretAlert(ctx, repo, d.Alert.Number)
	if err != nil {
		return nil, fmt.Errorf("ghas: fetch secret alert %s#%d: %w", repo, d.Alert.Number, err)
	}
	f, err := FindingFromSecretAlert(repo, alert)
	if err != nil {
		return nil, err
	}
	return []source.Finding{f}, nil
}

// List implements source.Lister, with Handler.List's filtering.
func (s *Secrets) List(
	ctx context.Context, repos []string, yield func(source.Finding) bool,
) (complete bool, err error) {
	var mapErr error
	complete, err = s.enum.Enumerate(ctx, repos, func(repo ghclient.Repo, alert *ghclient.SecretAlert) bool {
		if !ghclient.RepoMatches(repos, repo) {
			return true
		}
		f, err := FindingFromSecretAlert(repo, alert)
		if err != nil {
			mapErr = err
			return false
		}
		return yield(f)
	})
	if mapErr != nil {
		return false, mapErr
	}
	return complete, err
}

// Resolve implements source.Resolver: it resolves every secret alert it is
// handed with the verdict's reason, mapped onto GitHub's resolutions.
func (s *Secrets) Resolve(ctx context.Context, alerts []source.AlertRef, v source.Verdict) error {
	if v.Kind != source.VerdictIgnore || s.resolve == nil {
		return nil
	}
	if s.repo.Owner == "" || s.repo.Name == "" {
		return nil
	}
	resolution := SecretResolution(v.Reason)
	var errs []error
	for _, a := range alerts {
		num, err := strconv.Atoi(a.ID)
		if err != nil {
			errs = append(errs, fmt.Errorf("ghas: alert id %q is not a secret-scanning alert number", a.ID))
			continue
		}
		if err := s.resolve.ResolveSecretAlert(ctx, s.repo, num, resolution, v.Comment); err != nil {
			errs = append(errs, fmt.Errorf("ghas: resolve secret alert %s#%d: %w", s.repo, num, err))
		}
	}
	return errors.Join(errs...)
}

// SecretResolution maps a canonical verdict reason onto a secret-scanning
// resolution. Anything else, including empty, is a false positive: that is
// what an "ignore" verdict means by default.
func SecretResolution(reason string) string {
	switch reason {
	case source.ReasonUsedInTests:
		return ghclient.SecretResolutionUsedInTests
	case source.ReasonWontFix:
		return ghclient.SecretResolutionWontFix
	default:
		return ghclient.SecretResolutionFalsePositive
	}
}

// FindingFromSecretAlert normalizes one secret-scanning alert. Both the
// webhook path and the backfill lister use it. Nothing it reads can hold
// the secret value: ghclient.SecretAlert has no field for one.
func FindingFromSecretAlert(repo ghclient.Repo, alert *ghclient.SecretAlert) (source.Finding, error) {
	desc, err := templates.RenderGitHubSecretDescription(secretDescription(alert))
	if err != nil {
		return source.Finding{}, fmt.Errorf("ghas: render secret alert %s#%d: %w", repo, alert.Number, err)
	}
	secretType := alert.SecretType
	if secretType == "" {
		secretType = "unknown"
	}
	f := source.Finding{
		Source:      SecretsID,
		Category:    source.CategorySecret,
		Repo:        source.Repo{Owner: repo.Owner, Name: repo.Name},
		AlertNumber: alert.Number,
		Advisories:  []string{"secret:" + secretType},
		RuleID:      secretType,
		Title:       secretTitle(alert),
		Description: desc,
		Severity:    secretSeverity(alert),
		HTMLURL:     alert.HTMLURL,
	}
	for _, loc := range alert.Locations {
		if loc.Path == "" {
			continue
		}
		f.Locations = append(f.Locations, source.Location{
			Path:        loc.Path,
			StartLine:   loc.StartLine,
			EndLine:     loc.EndLine,
			StartColumn: loc.StartColumn,
			EndColumn:   loc.EndColumn,
		})
	}
	return f, nil
}

// secretSeverity rates a leak by what GitHub knows of it, since secret
// alerts carry no severity: a credential that still works, or that is
// already public, is critical; one the issuer has revoked is medium; one
// whose validity is unknown is high, because unknown is not safe.
func secretSeverity(a *ghclient.SecretAlert) string {
	switch {
	case strings.EqualFold(a.Validity, "active") || a.PubliclyLeaked:
		return "critical"
	case strings.EqualFold(a.Validity, "inactive"):
		return "medium"
	default:
		return "high"
	}
}

func secretTitle(a *ghclient.SecretAlert) string {
	name := a.SecretTypeDisplayName
	if name == "" {
		name = a.SecretType
	}
	if name == "" {
		name = "Secret"
	}
	return name + " exposed"
}

// secretDescription flattens the alert for the description template.
func secretDescription(a *ghclient.SecretAlert) templates.GitHubSecret {
	d := templates.GitHubSecret{
		DisplayName:            a.SecretTypeDisplayName,
		SecretType:             a.SecretType,
		Provider:               a.Provider,
		Validity:               a.Validity,
		PubliclyLeaked:         a.PubliclyLeaked,
		MultiRepo:              a.MultiRepo,
		PushProtectionBypassed: a.PushProtectionBypassed,
		URL:                    a.HTMLURL,
	}
	for i, loc := range a.Locations {
		if i == maxDescribedLocations {
			d.MoreLocations = len(a.Locations) - i
			break
		}
		d.Locations = append(d.Locations, templates.GitHubSecretLocation{
			Kind:      loc.Type,
			Path:      loc.Path,
			StartLine: loc.StartLine,
			CommitSHA: loc.CommitSHA,
			URL:       loc.URL,
		})
	}
	return d
}
