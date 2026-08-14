## patchy

Work with patchy security findings from the terminal

### Synopsis

patchy lists, inspects, reviews and acts on the custom resources that carry the
patchy pipeline's state machine.

It talks to the Kubernetes API with your own kubeconfig — never through a
controller or the status page — so what you can do is what your RBAC allows.
Run `patchy can-i` to see your grants.

### Options

```
  -A, --all-namespaces             work across every namespace
      --context string             kubeconfig context to use
  -h, --help                       help for patchy
      --kubeconfig string          path to the kubeconfig file
  -n, --namespace string           namespace to work in (default: the context's)
      --no-color                   disable colour and styling
  -o, --output string              output format: table, wide, json, yaml, name, or markdown (default "table")
      --request-timeout duration   timeout for a single API call (default 30s)
  -v, --verbose                    log what the CLI is doing to stderr
```

### SEE ALSO

* [patchy approve](patchy_approve.md)	 - Approve a finding, releasing a hold or reviving a handed-off finding
* [patchy backfill](patchy_backfill.md)	 - Backfill an integration's pre-existing open alerts into findings
* [patchy browse](patchy_browse.md)	 - Browse to a resource's page in a browser
* [patchy can-i](patchy_can-i.md)	 - Show which actions your RBAC allows
* [patchy completion](patchy_completion.md)	 - Generate the autocompletion script for the specified shell
* [patchy describe](patchy_describe.md)	 - Show the full detail of one resource
* [patchy dev](patchy_dev.md)	 - Local test harnesses for generic-integration authors
* [patchy expedite](patchy_expedite.md)	 - Expedite a finding past the accumulation window and the queue
* [patchy get](patchy_get.md)	 - List patchy resources
* [patchy mirror](patchy_mirror.md)	 - Mirror upstream helm charts and OCI artifacts into a platform registry
* [patchy resume](patchy_resume.md)	 - Resume a suspended finding
* [patchy retry](patchy_retry.md)	 - Retry a failed finding from the state it failed in
* [patchy review](patchy_review.md)	 - Read an agent's report on a finding
* [patchy suspend](patchy_suspend.md)	 - Suspend a finding, pausing its progress through the pipeline

