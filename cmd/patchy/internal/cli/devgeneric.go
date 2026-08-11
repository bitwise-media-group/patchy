// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package cli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/bitwise-media-group/patchy/cmd/patchy/internal/devharness"
)

// devGenericFlags carries the harness configuration; every field also
// resolves from PATCHY_DEV_* and .patchy.<ext> via loadCLIConfig.
type devGenericFlags struct {
	addr           string
	name           string
	secret         string
	secretFile     string
	minSeverity    string
	enhanceURL     string
	enhanceTimeout time.Duration
	resolveURL     string
	resolveTimeout time.Duration
	resolveDelay   time.Duration
	noAutoResolve  bool
}

// devGenericKeys maps the harness flags to their config keys.
var devGenericKeys = map[string]string{
	"addr":            "dev.addr",
	"name":            "dev.name",
	"secret":          "dev.secret",
	"secret-file":     "dev.secret-file",
	"min-severity":    "dev.min-severity",
	"enhance-url":     "dev.enhance-url",
	"enhance-timeout": "dev.enhance-timeout",
	"resolve-url":     "dev.resolve-url",
	"resolve-timeout": "dev.resolve-timeout",
	"resolve-delay":   "dev.resolve-delay",
	"no-auto-resolve": "dev.no-auto-resolve",
}

func newDevGenericCmd(opts *Options) *cobra.Command {
	f := &devGenericFlags{}
	cmd := &cobra.Command{
		Use:   "generic",
		Short: "Host a local receiver to test a generic integration",
		Long: "Run the generic webhook receiver on your workstation: the same server, HMAC\n" +
			"authentication, deduplication, and validation the integration-controller runs,\n" +
			"with findings kept in memory instead of becoming Finding resources. Each\n" +
			"ingested finding immediately exercises the outbound legs you configure — the\n" +
			"enhancer call, then the verdict write-back — so one signed POST tests all\n" +
			"three exchanges of the contract. No cluster is touched.\n\n" +
			"Rejected deliveries answer 401 with no reason on the wire, exactly as in\n" +
			"production; the reason appears here on stderr instead. Not emulated:\n" +
			"accumulation into Finding resources, tracking issues, and the investigation\n" +
			"that separates enhancement from dismissal — production sends the resolver\n" +
			"write-back only when a finding is dismissed, typically much later.\n\n" +
			"Without --secret or --secret-file a random secret is generated and printed,\n" +
			"so a first run needs no setup. Every flag also reads from a PATCHY_DEV_*\n" +
			"environment variable and a .patchy.yaml/.yml/.json in the working directory\n" +
			"(flag > environment > file > default).",
		Example: "  patchy dev generic\n" +
			"  patchy dev generic --secret dev-secret --enhance-url http://127.0.0.1:9000/enhance\n" +
			"  patchy dev generic --secret-file ./webhook.secret --resolve-url http://127.0.0.1:9000/resolve\n" +
			"  PATCHY_DEV_ENHANCE_URL=http://127.0.0.1:9000/enhance patchy dev generic\n" +
			"  patchy dev generic -o json | jq 'select(.kind==\"resolve\")'",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runDevGeneric(cmd, opts, f)
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.addr, "addr", "127.0.0.1:8100", "listen address (port 0 picks an ephemeral one)")
	fl.StringVar(&f.name, "name", "dev", "integration name: the webhook path segment and source id")
	fl.StringVar(&f.secret, "secret", "", "shared HMAC webhook secret")
	fl.StringVar(&f.secretFile, "secret-file", "", "file holding the shared HMAC webhook secret")
	fl.StringVar(&f.minSeverity, "min-severity", "", "drop findings below this severity: low, medium, high, or critical")
	fl.StringVar(&f.enhanceURL, "enhance-url", "", "enhancer endpoint to call for each ingested finding")
	fl.DurationVar(&f.enhanceTimeout, "enhance-timeout", 60*time.Second, "timeout per enhancer call")
	fl.StringVar(&f.resolveURL, "resolve-url", "", "resolver endpoint for the verdict write-back")
	fl.DurationVar(&f.resolveTimeout, "resolve-timeout", 60*time.Second, "timeout per resolver call")
	fl.DurationVar(&f.resolveDelay, "resolve-delay", 0, "pause between a finding's enhance and resolve calls")
	fl.BoolVar(&f.noAutoResolve, "no-auto-resolve", false, "only retain findings; resolve later with `patchy dev resolve`")
	cmd.MarkFlagsMutuallyExclusive("secret", "secret-file")
	_ = cmd.RegisterFlagCompletionFunc("min-severity", fixedCompletion(levelNames()))
	return cmd
}

func runDevGeneric(cmd *cobra.Command, opts *Options, f *devGenericFlags) error {
	v, err := loadCLIConfig(cmd, devGenericKeys)
	if err != nil {
		return err
	}
	f.addr = v.GetString("dev.addr")
	f.name = v.GetString("dev.name")
	f.secret = v.GetString("dev.secret")
	f.secretFile = v.GetString("dev.secret-file")
	f.minSeverity = v.GetString("dev.min-severity")
	f.enhanceURL = v.GetString("dev.enhance-url")
	f.enhanceTimeout = v.GetDuration("dev.enhance-timeout")
	f.resolveURL = v.GetString("dev.resolve-url")
	f.resolveTimeout = v.GetDuration("dev.resolve-timeout")
	f.resolveDelay = v.GetDuration("dev.resolve-delay")
	f.noAutoResolve = v.GetBool("dev.no-auto-resolve")
	noteConfigFile(opts, v)

	if err := validateDevName(f.name); err != nil {
		return err
	}
	if f.minSeverity != "" && !slices.Contains(levelNames(), f.minSeverity) {
		return errUsage(fmt.Errorf("min-severity %q is not %s", f.minSeverity, strings.Join(levelNames(), ", ")))
	}

	secret, provenance, err := resolveDevSecret(cmd, f.secret, f.secretFile)
	if err != nil {
		return err
	}
	if secret == nil {
		secret, err = generateDevSecret()
		if err != nil {
			return err
		}
		notef(opts.ErrOut, "patchy: generated webhook secret: %s\n", secret)
		notef(opts.ErrOut, "patchy: sign requests with %s: sha256=<hex of HMAC-SHA256(secret, body)>\n",
			"X-Patchy-Signature-256")
	} else {
		opts.debugf("webhook secret %s", provenance)
	}

	emit, err := devEmitter(opts)
	if err != nil {
		return err
	}
	level := slog.LevelInfo
	if opts.Verbose {
		level = slog.LevelDebug
	}
	h, err := devharness.New(devharness.Config{
		Addr:           f.addr,
		Name:           f.name,
		Secret:         secret,
		MinSeverity:    f.minSeverity,
		EnhanceURL:     f.enhanceURL,
		EnhanceTimeout: f.enhanceTimeout,
		ResolveURL:     f.resolveURL,
		ResolveTimeout: f.resolveTimeout,
		ResolveDelay:   f.resolveDelay,
		AutoResolve:    !f.noAutoResolve,
		Log:            slog.New(slog.NewTextHandler(opts.ErrOut, &slog.HandlerOptions{Level: level})),
		Events:         emit,
	})
	if err != nil {
		return err
	}

	err = h.Run(cmd.Context())
	sum := h.Summary()
	notef(opts.ErrOut, "patchy: %d deliveries, %d findings, %d enhance calls (%d failed), %d resolve calls (%d failed)\n",
		sum.Deliveries, sum.Findings, sum.EnhanceCalls, sum.EnhanceFailures, sum.ResolveCalls, sum.ResolveFailures)
	if f.noAutoResolve && sum.Findings > 0 {
		notef(opts.ErrOut, "patchy: findings were retained without a write-back; "+
			"replay the payload through `patchy dev resolve` when ready\n")
	}
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

// validateDevName enforces the same shape a generic Integration's name must
// have: it becomes the path segment, the source id, and a label value.
func validateDevName(name string) error {
	switch {
	case name == "":
		return errUsage(errors.New("an integration name is required"))
	case strings.Contains(name, "/"):
		return errUsage(fmt.Errorf("integration name %q cannot contain a slash: it is a webhook path segment", name))
	case len(name) > 63:
		return errUsage(fmt.Errorf("integration name %q exceeds 63 characters, the label-value limit", name))
	}
	return nil
}

// generateDevSecret mints a random hex secret so a first run needs no setup.
// Hex, so the printed value pastes cleanly into a shell or a signing script.
func generateDevSecret() ([]byte, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, fmt.Errorf("generate secret: %w", err)
	}
	return []byte(hex.EncodeToString(raw)), nil
}
