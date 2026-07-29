// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package awsinv

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/configservice"
	"github.com/aws/aws-sdk-go-v2/service/resourceexplorer2"
	"github.com/aws/aws-sdk-go-v2/service/resourceexplorer2/document"
	retypes "github.com/aws/aws-sdk-go-v2/service/resourceexplorer2/types"
	"github.com/aws/smithy-go"
)

const bucketARN = "arn:aws:s3:::acme-legacy-assets"

type fakeConfig struct {
	expr    string
	results []string
	err     error
}

func (f *fakeConfig) SelectAggregateResourceConfig(_ context.Context,
	in *configservice.SelectAggregateResourceConfigInput,
	_ ...func(*configservice.Options)) (*configservice.SelectAggregateResourceConfigOutput, error) {
	f.expr = aws.ToString(in.Expression)
	if f.err != nil {
		return nil, f.err
	}
	return &configservice.SelectAggregateResourceConfigOutput{Results: f.results}, nil
}

func (f *fakeConfig) DescribeConfigurationAggregators(_ context.Context,
	_ *configservice.DescribeConfigurationAggregatorsInput,
	_ ...func(*configservice.Options)) (*configservice.DescribeConfigurationAggregatorsOutput, error) {
	return &configservice.DescribeConfigurationAggregatorsOutput{}, nil
}

type fakeExplorer struct {
	query     string
	view      string
	resources []retypes.Resource
	err       error
	included  []string
}

func (f *fakeExplorer) Search(_ context.Context, in *resourceexplorer2.SearchInput,
	_ ...func(*resourceexplorer2.Options)) (*resourceexplorer2.SearchOutput, error) {
	f.query, f.view = aws.ToString(in.QueryString), aws.ToString(in.ViewArn)
	if f.err != nil {
		return nil, f.err
	}
	return &resourceexplorer2.SearchOutput{Resources: f.resources}, nil
}

func (f *fakeExplorer) GetView(_ context.Context, _ *resourceexplorer2.GetViewInput,
	_ ...func(*resourceexplorer2.Options)) (*resourceexplorer2.GetViewOutput, error) {
	if f.err != nil {
		return nil, f.err
	}
	view := &retypes.View{}
	for _, name := range f.included {
		view.IncludedProperties = append(view.IncludedProperties,
			retypes.IncludedProperty{Name: aws.String(name)})
	}
	return &resourceexplorer2.GetViewOutput{View: view}, nil
}

type apiError string

func (e apiError) Error() string                 { return string(e) }
func (e apiError) ErrorCode() string             { return string(e) }
func (e apiError) ErrorMessage() string          { return string(e) }
func (e apiError) ErrorFault() smithy.ErrorFault { return smithy.FaultClient }

func TestConfigValidate(t *testing.T) {
	aggregator := &ConfigAggregator{Name: "org", Region: "eu-west-2"}
	explorer := &ResourceExplorer{ViewARN: "arn:aws:resource-explorer-2:eu-west-2:123456789012:view/org/abc"}
	tests := []struct {
		name string
		cfg  Config
		ok   bool
	}{
		{"config aggregator", Config{ConfigAggregator: aggregator}, true},
		{"resource explorer", Config{ResourceExplorer: explorer}, true},
		{"neither backend", Config{}, false},
		{"both backends", Config{ConfigAggregator: aggregator, ResourceExplorer: explorer}, false},
		{"aggregator without region", Config{ConfigAggregator: &ConfigAggregator{Name: "org"}}, false},
		{"view ARN of another service", Config{ResourceExplorer: &ResourceExplorer{
			ViewARN: "arn:aws:s3:::acme-legacy-assets"}}, false},
		{"view ARN without region", Config{ResourceExplorer: &ResourceExplorer{
			ViewARN: "arn:aws:resource-explorer-2::123456789012:view/org/abc"}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.cfg.Validate(); (err == nil) != tt.ok {
				t.Fatalf("Validate() = %v, want ok=%v", err, tt.ok)
			}
		})
	}
}

func TestConfigTags(t *testing.T) {
	row := func(tags string) string {
		return fmt.Sprintf(`{"resourceName":"acme-legacy-assets","tags":%s}`, tags)
	}
	tests := []struct {
		name    string
		results []string
		want    map[string]string
	}{
		{"key value list", []string{row(`[{"key":"owner","value":"platform"},{"key":"env","value":"prod"}]`)},
			map[string]string{"owner": "platform", "env": "prod"}},
		{"capitalized pairs", []string{row(`[{"Key":"owner","Value":"platform"}]`)},
			map[string]string{"owner": "platform"}},
		{"map form", []string{row(`{"owner":"platform"}`)},
			map[string]string{"owner": "platform"}},
		{"untagged resource", []string{row(`[]`)}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := &fakeConfig{results: tt.results}
			c := &Client{aggregator: "org", config: api}
			got, err := c.TagsFor(t.Context(), bucketARN)
			if err != nil {
				t.Fatalf("TagsFor() error = %v", err)
			}
			if got.Name != "acme-legacy-assets" {
				t.Errorf("Name = %q, want acme-legacy-assets", got.Name)
			}
			if !maps.Equal(got.Tags, tt.want) {
				t.Errorf("Tags = %v, want %v", got.Tags, tt.want)
			}
		})
	}
}

func TestConfigTagsQuery(t *testing.T) {
	api := &fakeConfig{results: []string{`{"resourceName":"it"}`}}
	c := &Client{aggregator: "org", config: api}
	if _, err := c.TagsFor(t.Context(), "arn:aws:s3:::it's"); err != nil {
		t.Fatalf("TagsFor() error = %v", err)
	}
	want := `SELECT resourceName, tags WHERE arn = 'arn:aws:s3:::it''s'`
	if api.expr != want {
		t.Errorf("expression = %q, want %q", api.expr, want)
	}
}

func TestConfigTagsErrors(t *testing.T) {
	tests := []struct {
		name     string
		api      *fakeConfig
		notFound bool
	}{
		{"no rows is final", &fakeConfig{}, true},
		{"invalid expression is final", &fakeConfig{err: apiError("InvalidExpressionException")}, true},
		{"access denied is retryable", &fakeConfig{err: apiError("AccessDeniedException")}, false},
		{"missing aggregator is retryable", &fakeConfig{err: apiError("NoSuchConfigurationAggregatorException")}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &Client{aggregator: "org", config: tt.api}
			_, err := c.TagsFor(t.Context(), bucketARN)
			if err == nil {
				t.Fatal("TagsFor() expected an error")
			}
			if errors.Is(err, ErrNotFound) != tt.notFound {
				t.Errorf("errors.Is(err, ErrNotFound) = %v, want %v (err %v)",
					!tt.notFound, tt.notFound, err)
			}
		})
	}
}

func TestExplorerTags(t *testing.T) {
	resource := func(arn string, tags any) retypes.Resource {
		r := retypes.Resource{Arn: aws.String(arn)}
		if tags != nil {
			r.Properties = []retypes.ResourceProperty{{
				Name: aws.String("tags"),
				Data: document.NewLazyDocument(tags),
			}}
		}
		return r
	}
	tests := []struct {
		name      string
		resources []retypes.Resource
		want      map[string]string
	}{
		{"tags decoded", []retypes.Resource{resource(bucketARN,
			[]map[string]string{{"Key": "owner", "Value": "platform"}})},
			map[string]string{"owner": "platform"}},
		{"near-miss hits skipped", []retypes.Resource{
			resource(bucketARN+"-copy", []map[string]string{{"Key": "owner", "Value": "other"}}),
			resource(bucketARN, []map[string]string{{"Key": "owner", "Value": "platform"}}),
		}, map[string]string{"owner": "platform"}},
		{"resource without tags property", []retypes.Resource{resource(bucketARN, nil)}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := &fakeExplorer{resources: tt.resources}
			c := &Client{viewARN: "arn:aws:resource-explorer-2:eu-west-2:123456789012:view/org/abc", explorer: api}
			got, err := c.TagsFor(t.Context(), bucketARN)
			if err != nil {
				t.Fatalf("TagsFor() error = %v", err)
			}
			if !maps.Equal(got.Tags, tt.want) {
				t.Errorf("Tags = %v, want %v", got.Tags, tt.want)
			}
			if api.query != "id:"+bucketARN {
				t.Errorf("query = %q, want id:%s", api.query, bucketARN)
			}
			if api.view != c.viewARN {
				t.Errorf("view = %q, want %q", api.view, c.viewARN)
			}
		})
	}
}

func TestExplorerTagsErrors(t *testing.T) {
	tests := []struct {
		name     string
		api      *fakeExplorer
		notFound bool
	}{
		{"no hits is final", &fakeExplorer{}, true},
		{"only near-miss hits is final", &fakeExplorer{resources: []retypes.Resource{
			{Arn: aws.String(bucketARN + "-copy")}}}, true},
		{"missing view is retryable", &fakeExplorer{err: apiError("ResourceNotFoundException")}, false},
		{"unauthorized is retryable", &fakeExplorer{err: apiError("UnauthorizedException")}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &Client{viewARN: "arn:aws:resource-explorer-2:eu-west-2:123456789012:view/org/abc",
				explorer: tt.api}
			_, err := c.TagsFor(t.Context(), bucketARN)
			if err == nil {
				t.Fatal("TagsFor() expected an error")
			}
			if errors.Is(err, ErrNotFound) != tt.notFound {
				t.Errorf("errors.Is(err, ErrNotFound) = %v, want %v (err %v)",
					!tt.notFound, tt.notFound, err)
			}
		})
	}
}

func TestVerifyView(t *testing.T) {
	tests := []struct {
		name     string
		included []string
		ok       bool
	}{
		{"view with tags", []string{"tags"}, true},
		{"view without tags", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &Client{viewARN: "arn:aws:resource-explorer-2:eu-west-2:123456789012:view/org/abc",
				explorer: &fakeExplorer{included: tt.included}}
			if err := c.verify(t.Context()); (err == nil) != tt.ok {
				t.Fatalf("verify() = %v, want ok=%v", err, tt.ok)
			}
		})
	}
}

func TestEmptyARNIsNotFound(t *testing.T) {
	c := &Client{aggregator: "org", config: &fakeConfig{}}
	if _, err := c.TagsFor(t.Context(), ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("TagsFor(\"\") = %v, want ErrNotFound", err)
	}
}
