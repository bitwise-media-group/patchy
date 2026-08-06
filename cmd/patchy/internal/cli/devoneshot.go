// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/bitwise-media-group/patchy/cmd/patchy/internal/devharness"
	"github.com/bitwise-media-group/patchy/pkg/source"
)

// devOneShotFlags carries the shared configuration of the one-shot outbound
// commands. Unlike the harness, the secret is required: the endpoint being
// tested must already share it to verify signatures.
type devOneShotFlags struct {
	url        string
	name       string
	secret     string
	secretFile string
	timeout    time.Duration
}

// devOneShotKeys maps the one-shot flags onto the same config keys the
// harness uses, per leg — one .patchy.yaml drives both commands.
func devOneShotKeys(leg string) map[string]string {
	return map[string]string{
		"url":         "dev." + leg + "-url",
		"name":        "dev.name",
		"secret":      "dev.secret",
		"secret-file": "dev.secret-file",
		"timeout":     "dev." + leg + "-timeout",
	}
}

func newDevEnhanceCmd(opts *Options) *cobra.Command {
	f := &devOneShotFlags{}
	cmd := &cobra.Command{
		Use:   "enhance <payload-file>...",
		Short: "Send an enhancement request to a generic enhancer",
		Long: "Fire one signed enhancement request per finding at an enhancer endpoint,\n" +
			"without hosting anything: the way to test an integration that is only an\n" +
			"enhancer and never delivers findings itself.\n\n" +
			"Payload files use the same envelope the webhook receives (version v1, event\n" +
			"findings), validated by the same code, so a fixture works in both places. The\n" +
			"request carries exactly what production sends for a fresh finding: title,\n" +
			"description, repository and cloud resource — no issue number, no labels.\n\n" +
			"Flags also resolve from PATCHY_DEV_* and .patchy.yaml/.yml/.json (the same\n" +
			"keys the harness reads: dev.enhance-url, dev.secret-file, ...).",
		Example: "  patchy dev enhance --url http://127.0.0.1:9000/enhance --secret dev-secret findings.json\n" +
			"  patchy dev enhance --secret-file ./webhook.secret findings.json   # url from .patchy.yaml\n" +
			"  patchy dev enhance --url http://127.0.0.1:9000/enhance --secret s -o json findings.json | jq .",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDevOneShot(cmd, opts, f, "enhance", args)
		},
	}
	bindDevOneShotFlags(cmd, f, "enhancer endpoint to call")
	return cmd
}

func newDevResolveCmd(opts *Options) *cobra.Command {
	f := &devOneShotFlags{}
	cmd := &cobra.Command{
		Use:   "resolve <payload-file>...",
		Short: "Send a verdict write-back to a generic resolver",
		Long: "Fire one signed verdict write-back per finding at a resolver endpoint. The\n" +
			"verdict is the one patchy sends in production — ignore, \"false positive\" —\n" +
			"because dismissal after investigation is the only path that resolves today.\n\n" +
			"Production sends the write-back once per finding at dismissal, carrying every\n" +
			"accumulated alert; this command does not emulate accumulation, so it sends one\n" +
			"alert per call. Any 2xx answer is success, and patchy retries failures — the\n" +
			"endpoint must treat an already-closed alert as success, not an error.\n\n" +
			"Payload files use the same envelope the webhook receives; flags also resolve\n" +
			"from PATCHY_DEV_* and .patchy.yaml/.yml/.json.",
		Example: "  patchy dev resolve --url http://127.0.0.1:9000/resolve --secret dev-secret findings.json\n" +
			"  patchy dev resolve --secret-file ./webhook.secret findings.json   # url from .patchy.yaml",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDevOneShot(cmd, opts, f, "resolve", args)
		},
	}
	bindDevOneShotFlags(cmd, f, "resolver endpoint to call")
	return cmd
}

// bindDevOneShotFlags registers the shared one-shot flag set.
func bindDevOneShotFlags(cmd *cobra.Command, f *devOneShotFlags, urlHelp string) {
	fl := cmd.Flags()
	fl.StringVar(&f.url, "url", "", urlHelp)
	fl.StringVar(&f.name, "name", "dev", "integration name sent as the request's integration field")
	fl.StringVar(&f.secret, "secret", "", "shared HMAC secret the endpoint verifies")
	fl.StringVar(&f.secretFile, "secret-file", "", "file holding the shared HMAC secret")
	fl.DurationVar(&f.timeout, "timeout", 60*time.Second, "timeout per call")
	cmd.MarkFlagsMutuallyExclusive("secret", "secret-file")
}

func runDevOneShot(cmd *cobra.Command, opts *Options, f *devOneShotFlags, leg string, files []string) error {
	v, err := loadDevConfig(cmd, devOneShotKeys(leg))
	if err != nil {
		return err
	}
	f.url = v.GetString("dev." + leg + "-url")
	f.name = v.GetString("dev.name")
	f.secret = v.GetString("dev.secret")
	f.secretFile = v.GetString("dev.secret-file")
	f.timeout = v.GetDuration("dev." + leg + "-timeout")
	noteDevConfig(opts, v)

	if err := validateDevName(f.name); err != nil {
		return err
	}
	if f.url == "" {
		return errUsage(fmt.Errorf("a %s url is required: pass --url, set PATCHY_DEV_%s_URL, or configure dev.%s-url",
			leg, strings.ToUpper(leg), leg))
	}
	secret, _, err := resolveDevSecret(cmd, f.secret, f.secretFile)
	if err != nil {
		return err
	}
	if secret == nil {
		return errUsage(errors.New(
			"a shared secret is required: the endpoint must verify the signature, so pass --secret or --secret-file"))
	}

	findings, err := loadDevFindings(cmd.Context(), f.name, files)
	if err != nil {
		return err
	}
	client, err := devharness.NewClient(f.url, secret, f.timeout)
	if err != nil {
		return err
	}
	emit, err := devEmitter(opts)
	if err != nil {
		return err
	}
	if leg == "enhance" {
		return devharness.Enhance(cmd.Context(), client, f.name, findings, emit)
	}
	return devharness.Resolve(cmd.Context(), client, f.name, findings, emit)
}

// loadDevFindings parses every payload file through the production
// validation path, so a malformed fixture fails the same way a malformed
// delivery would.
func loadDevFindings(ctx context.Context, name string, files []string) ([]source.Finding, error) {
	var out []source.Finding
	for _, path := range files {
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read payload: %w", err)
		}
		findings, err := devharness.ParseFindings(ctx, name, raw)
		if err != nil {
			return nil, errUsage(fmt.Errorf("%s: %w", path, err))
		}
		out = append(out, findings...)
	}
	if len(out) == 0 {
		return nil, errUsage(errors.New("the payload(s) carried no findings"))
	}
	return out, nil
}
