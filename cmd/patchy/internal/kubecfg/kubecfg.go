// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package kubecfg

import (
	"context"
	"encoding/json"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/internal/kube"
)

// tableAccept asks the API server to render the response as a Table using the
// CRD's additionalPrinterColumns, falling back to the object itself on a server
// that cannot. This is the same negotiation kubectl performs.
const tableAccept = "application/json;as=Table;v=v1;g=meta.k8s.io,application/json"

// Env is a resolved connection to a cluster: what to talk to, as whom, and
// where to look by default.
type Env struct {
	// Client is a cache-less client for the patchy kinds and the built-ins.
	Client client.Client
	// Config is the REST config the client was built from, kept for the
	// hand-rolled Table requests.
	Config *rest.Config
	// Namespace is the effective namespace: the --namespace flag if given,
	// otherwise whatever the kubeconfig context selects. Empty means every
	// namespace (--all-namespaces).
	Namespace string
}

// Options are the connection inputs, straight from the persistent flags.
type Options struct {
	// Kubeconfig is an explicit kubeconfig path; empty uses the standard
	// $KUBECONFIG / ~/.kube/config chain.
	Kubeconfig string
	// Context overrides the kubeconfig's current-context.
	Context string
	// Namespace overrides the context's namespace.
	Namespace string
	// AllNamespaces widens every read to the whole cluster.
	AllNamespaces bool
}

// Connect resolves the kubeconfig and builds the client. It performs no
// requests, so a bad cluster address surfaces on first use rather than here.
func Connect(opts Options) (*Env, error) {
	cfg, ctxNamespace, err := kube.ClientConfig(opts.Kubeconfig, opts.Context)
	if err != nil {
		return nil, err
	}

	c, err := client.New(cfg, client.Options{Scheme: kube.Scheme()})
	if err != nil {
		return nil, fmt.Errorf("kubernetes client: %w", err)
	}

	ns := ctxNamespace
	if opts.Namespace != "" {
		ns = opts.Namespace
	}
	if opts.AllNamespaces {
		ns = ""
	}
	return &Env{Client: c, Config: cfg, Namespace: ns}, nil
}

// Scope describes where a request looks: a namespace, or every namespace.
func (e *Env) Scope() string {
	if e.Namespace == "" {
		return "all namespaces"
	}
	return "namespace " + e.Namespace
}

// Table fetches a server-rendered table for the given resource. names, when
// non-empty, narrows the request to those objects; selector is an optional
// label selector.
//
// The rows carry the CRD's own print columns, so nothing client-side decides
// what a Finding looks like in a list.
func (e *Env) Table(ctx context.Context, plural string, names []string, selector string) (*metav1.Table, error) {
	rc, err := e.restClient()
	if err != nil {
		return nil, err
	}

	// A single name is a Get; anything else is a List, narrowed client-side
	// afterwards. The API server has no "these three names" list form.
	req := rc.Get().Resource(plural)
	if e.Namespace != "" {
		req = req.Namespace(e.Namespace)
	}
	if len(names) == 1 {
		req = req.Name(names[0])
	} else if selector != "" {
		req = req.Param("labelSelector", selector)
	}
	// Ask for each row's object metadata alongside the rendered cells. It costs
	// one field per row and buys real creationTimestamp ordering and reliable
	// name matching, instead of trying to parse them back out of a rendered
	// "5d" age cell.
	//
	// Set as raw query parameters rather than through VersionedParams: that
	// route encodes the options struct via a scheme, and neither TableOptions
	// (a meta type) nor this CRD's group is registered in client-go's. These
	// two strings are the wire contract either way.
	req = req.Param("includeObject", string(metav1.IncludeMetadata))

	raw, err := req.SetHeader("Accept", tableAccept).Do(ctx).Raw()
	if err != nil {
		return nil, err
	}
	var table metav1.Table
	if err := json.Unmarshal(raw, &table); err != nil {
		return nil, fmt.Errorf("decode table for %s: %w", plural, err)
	}
	return &table, nil
}

// RowMeta decodes the object metadata the server attached to a table row.
// Returns nil when the row carries none, which callers treat as "unknown"
// rather than an error — a table is still printable without it.
func RowMeta(row metav1.TableRow) *metav1.PartialObjectMetadata {
	if len(row.Object.Raw) == 0 {
		return nil
	}
	var meta metav1.PartialObjectMetadata
	if err := json.Unmarshal(row.Object.Raw, &meta); err != nil {
		return nil
	}
	return &meta
}

// RowName returns a row's object name, or "" when the row carries no metadata.
func RowName(row metav1.TableRow) string {
	if m := RowMeta(row); m != nil {
		return m.Name
	}
	return ""
}

// restClient builds a REST client for the patchy API group. Table responses are
// decoded by hand, so the codec here only has to satisfy the client's
// constructor.
func (e *Env) restClient() (*rest.RESTClient, error) {
	cfg := rest.CopyConfig(e.Config)
	gv := v1alpha1.GroupVersion
	cfg.GroupVersion = &gv
	cfg.APIPath = "/apis"
	cfg.NegotiatedSerializer = scheme.Codecs.WithoutConversion()

	rc, err := rest.RESTClientFor(cfg)
	if err != nil {
		return nil, fmt.Errorf("rest client: %w", err)
	}
	return rc, nil
}
