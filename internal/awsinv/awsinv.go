// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package awsinv

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/configservice"
	"github.com/aws/aws-sdk-go-v2/service/resourceexplorer2"
	"github.com/aws/smithy-go"
)

// ErrNotFound reports that the inventory has no record of the resource. This
// is a permanent answer, not a transient one: the resource may have been
// deleted between the finding being raised and this lookup, it may be of a
// type the inventory does not record, or it may not be indexed yet. Callers
// use it to decide against retrying.
var ErrNotFound = errors.New("resource not found in aws inventory")

// Tags are one resource's tags, plus enough identity to log about it.
type Tags struct {
	// Name is the resource's own name when the inventory reports one, else
	// its ARN.
	Name string
	// Tags are the resource's tags.
	Tags map[string]string
}

// ConfigAggregator names the AWS Config configuration aggregator to query.
type ConfigAggregator struct {
	// Name of the aggregator.
	Name string
	// Region the aggregator lives in.
	Region string
}

// ResourceExplorer names the Resource Explorer view to search. The region
// the view lives in is read from its ARN.
type ResourceExplorer struct {
	// ViewARN of the organization-wide view.
	ViewARN string
}

// Config selects exactly one inventory backend.
type Config struct {
	// ConfigAggregator queries an AWS Config aggregator.
	ConfigAggregator *ConfigAggregator
	// ResourceExplorer searches a Resource Explorer view.
	ResourceExplorer *ResourceExplorer
}

// Validate reports whether the configuration names exactly one usable
// backend. Checked at construction so a bad Integration fails at client
// build rather than on the first finding.
func (c Config) Validate() error {
	switch {
	case c.ConfigAggregator != nil && c.ResourceExplorer != nil:
		return errors.New("awsinv: configAggregator and resourceExplorer are mutually exclusive")
	case c.ConfigAggregator != nil:
		if c.ConfigAggregator.Name == "" || c.ConfigAggregator.Region == "" {
			return errors.New("awsinv: configAggregator needs a name and a region")
		}
		return nil
	case c.ResourceExplorer != nil:
		_, err := viewRegion(c.ResourceExplorer.ViewARN)
		return err
	default:
		return errors.New("awsinv: one of configAggregator or resourceExplorer is required")
	}
}

// region the SDK client must dial: the aggregator's, or the view ARN's.
func (c Config) region() (string, error) {
	if c.ConfigAggregator != nil {
		return c.ConfigAggregator.Region, nil
	}
	return viewRegion(c.ResourceExplorer.ViewARN)
}

// viewRegion reads the region out of a Resource Explorer view ARN
// (arn:partition:resource-explorer-2:region:account:view/...).
func viewRegion(arn string) (string, error) {
	parts := strings.SplitN(arn, ":", 6)
	if len(parts) == 6 && parts[2] == "resource-explorer-2" && parts[3] != "" &&
		strings.HasPrefix(parts[5], "view/") {
		return parts[3], nil
	}
	return "", fmt.Errorf(
		"awsinv: view ARN %q must be arn:<partition>:resource-explorer-2:<region>:<account>:view/<name>/<id>", arn)
}

// configAPI is the slice of the AWS Config client the backend uses; a seam
// for tests, which must not dial AWS.
type configAPI interface {
	SelectAggregateResourceConfig(ctx context.Context, in *configservice.SelectAggregateResourceConfigInput,
		opts ...func(*configservice.Options)) (*configservice.SelectAggregateResourceConfigOutput, error)
	DescribeConfigurationAggregators(ctx context.Context, in *configservice.DescribeConfigurationAggregatorsInput,
		opts ...func(*configservice.Options)) (*configservice.DescribeConfigurationAggregatorsOutput, error)
}

// explorerAPI is the slice of the Resource Explorer client the backend uses.
type explorerAPI interface {
	Search(ctx context.Context, in *resourceexplorer2.SearchInput,
		opts ...func(*resourceexplorer2.Options)) (*resourceexplorer2.SearchOutput, error)
	GetView(ctx context.Context, in *resourceexplorer2.GetViewInput,
		opts ...func(*resourceexplorer2.Options)) (*resourceexplorer2.GetViewOutput, error)
}

// Client reads resource tags from one of the two inventories. There is no
// interface here on purpose: the consumer declares the one-method seam it
// needs and this satisfies it.
type Client struct {
	aggregator string
	config     configAPI
	viewARN    string
	explorer   explorerAPI
}

// New builds a client for the configured backend and verifies the backend is
// usable — the aggregator exists, or the view exists and includes the tags
// property (without it every lookup would "succeed" tagless, which must fail
// loudly here instead). Credentials come from the SDK default chain — EKS
// Pod Identity or IRSA on EKS, web-identity federation elsewhere — so no key
// material exists anywhere.
func New(ctx context.Context, cfg Config) (*Client, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	region, err := cfg.region()
	if err != nil {
		return nil, err
	}
	sdk, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
	if err != nil {
		return nil, fmt.Errorf("awsinv: load credentials: %w", err)
	}
	c := &Client{}
	if cfg.ConfigAggregator != nil {
		c.aggregator = cfg.ConfigAggregator.Name
		c.config = configservice.NewFromConfig(sdk)
	} else {
		c.viewARN = cfg.ResourceExplorer.ViewARN
		c.explorer = resourceexplorer2.NewFromConfig(sdk)
	}
	if err := c.verify(ctx); err != nil {
		return nil, err
	}
	return c, nil
}

// Close releases nothing — the SDK clients hold no connection of their own —
// and exists so the enhancer can treat both cloud clients alike.
func (c *Client) Close() error { return nil }

// verify asks the backend whether it can answer at all.
func (c *Client) verify(ctx context.Context) error {
	if c.config != nil {
		out, err := c.config.DescribeConfigurationAggregators(ctx,
			&configservice.DescribeConfigurationAggregatorsInput{
				ConfigurationAggregatorNames: []string{c.aggregator},
			})
		if err != nil {
			return fmt.Errorf("awsinv: describe aggregator %s: %w", c.aggregator, err)
		}
		if len(out.ConfigurationAggregators) == 0 {
			return fmt.Errorf("awsinv: aggregator %s does not exist", c.aggregator)
		}
		return nil
	}
	out, err := c.explorer.GetView(ctx, &resourceexplorer2.GetViewInput{ViewArn: aws.String(c.viewARN)})
	if err != nil {
		return fmt.Errorf("awsinv: get view %s: %w", c.viewARN, err)
	}
	if out.View != nil {
		for _, p := range out.View.IncludedProperties {
			if strings.EqualFold(aws.ToString(p.Name), "tags") {
				return nil
			}
		}
	}
	return fmt.Errorf(
		"awsinv: view %s does not include the tags property; every lookup would come back tagless", c.viewARN)
}

// TagsFor returns the tags of one resource, named by its ARN. The ARN is an
// exact identifier in both inventories, so there is no fallback search: a
// miss means the resource is unrecorded, not misspelled.
//
// Errors are classified for the caller: ErrNotFound is final, everything
// else is worth retrying. That distinction is what lets the enhancer chain
// decide between advancing a finding and holding it for another try.
func (c *Client) TagsFor(ctx context.Context, arn string) (*Tags, error) {
	if arn == "" {
		return nil, ErrNotFound
	}
	if c.config != nil {
		return c.configTags(ctx, arn)
	}
	return c.explorerTags(ctx, arn)
}

// configTags runs one aggregator query. tags and arn are first-class
// properties of the configuration-item schema, so this is an exact SQL
// lookup.
func (c *Client) configTags(ctx context.Context, arn string) (*Tags, error) {
	expr := "SELECT resourceName, tags WHERE arn = '" + strings.ReplaceAll(arn, "'", "''") + "'"
	out, err := c.config.SelectAggregateResourceConfig(ctx,
		&configservice.SelectAggregateResourceConfigInput{
			ConfigurationAggregatorName: aws.String(c.aggregator),
			Expression:                  aws.String(expr),
			Limit:                       1,
		})
	if err != nil {
		return nil, classify(arn, err)
	}
	if len(out.Results) == 0 {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, arn)
	}
	var row struct {
		ResourceName string `json:"resourceName"`
		Tags         any    `json:"tags"`
	}
	if err := json.Unmarshal([]byte(out.Results[0]), &row); err != nil {
		return nil, fmt.Errorf("awsinv: lookup %s: decode result: %w", arn, err)
	}
	name := row.ResourceName
	if name == "" {
		name = arn
	}
	return &Tags{Name: name, Tags: normalizeTags(row.Tags)}, nil
}

// explorerTags runs one view search. id: is the exact-identifier filter, but
// the search itself still ranks rather than matches, so the ARN is compared
// against each hit before any of them counts.
func (c *Client) explorerTags(ctx context.Context, arn string) (*Tags, error) {
	out, err := c.explorer.Search(ctx, &resourceexplorer2.SearchInput{
		QueryString: aws.String("id:" + arn),
		ViewArn:     aws.String(c.viewARN),
	})
	if err != nil {
		return nil, classify(arn, err)
	}
	for _, res := range out.Resources {
		if !strings.EqualFold(aws.ToString(res.Arn), arn) {
			continue
		}
		tags := map[string]string{}
		for _, prop := range res.Properties {
			if !strings.EqualFold(aws.ToString(prop.Name), "tags") || prop.Data == nil {
				continue
			}
			raw, err := prop.Data.MarshalSmithyDocument()
			if err != nil {
				return nil, fmt.Errorf("awsinv: lookup %s: decode tags: %w", arn, err)
			}
			var v any
			if err := json.Unmarshal(raw, &v); err != nil {
				return nil, fmt.Errorf("awsinv: lookup %s: decode tags: %w", arn, err)
			}
			maps.Copy(tags, normalizeTags(v))
		}
		return &Tags{Name: arn, Tags: tags}, nil
	}
	return nil, fmt.Errorf("%w: %s", ErrNotFound, arn)
}

// normalizeTags accepts the shapes the two inventories spell tags in — a
// key/value object list (either casing) or a plain map — and returns a map.
func normalizeTags(v any) map[string]string {
	tags := map[string]string{}
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			if s, ok := val.(string); ok {
				tags[k] = s
			}
		}
	case []any:
		for _, e := range t {
			pair, ok := e.(map[string]any)
			if !ok {
				continue
			}
			k, _ := first(pair, "key", "Key").(string)
			val, _ := first(pair, "value", "Value").(string)
			if k != "" {
				tags[k] = val
			}
		}
	}
	if len(tags) == 0 {
		return nil
	}
	return tags
}

// first returns the first present key's value.
func first(m map[string]any, keys ...string) any {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			return v
		}
	}
	return nil
}

// classify wraps an inventory error, marking the permanent ones so a caller
// does not retry something that will never succeed. Everything else —
// including AccessDenied, which is usually an identity binding that has not
// propagated yet, and a missing aggregator or view, which an operator can
// still fix — stays retryable.
func classify(arn string, err error) error {
	var api smithy.APIError
	if errors.As(err, &api) && api.ErrorCode() == "InvalidExpressionException" {
		// The only way the generated query is invalid is the identifier
		// itself; retrying the same ARN cannot go differently.
		return fmt.Errorf("%w: %s", ErrNotFound, arn)
	}
	return fmt.Errorf("awsinv: lookup %s: %w", arn, err)
}
