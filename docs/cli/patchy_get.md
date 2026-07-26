## patchy get

List patchy resources

### Synopsis

List patchy resources.

Columns come from the CRDs themselves, so they match `kubectl get` exactly;
-o wide adds the columns marked lower priority (issue and pull-request links).

```
patchy get <resource> [name...] [flags]
```

### Examples

```
  patchy get findings
  patchy get findings --phase AwaitingApproval --severity critical
  patchy get findings --awaiting -o wide
  patchy get investigations --finding my-finding
  patchy get fnd my-finding -o yaml
```

### Options

```
      --awaiting           only findings with an action available
      --finding string     only runs belonging to this finding
  -h, --help               help for get
      --phase strings      only findings in these phases
      --priority strings   only findings at these priorities
      --repo string        only findings whose repository name contains this
  -l, --selector string    label selector (server-side)
      --severity strings   only these severities: low, medium, high, critical
      --sort-by string     sort by: age, name, severity, priority, or phase (default "age")
      --source string      only findings from this source handler
      --suspended          only suspended findings
      --verdict strings    only findings with these verdicts: remediate, ignore, manual
```

### Options inherited from parent commands

```
  -A, --all-namespaces             work across every namespace
      --context string             kubeconfig context to use
      --kubeconfig string          path to the kubeconfig file
  -n, --namespace string           namespace to work in (default: the context's)
      --no-color                   disable colour and styling
  -o, --output string              output format: table, wide, json, yaml, name, or markdown (default "table")
      --request-timeout duration   timeout for a single API call (default 30s)
  -v, --verbose                    log what the CLI is doing to stderr
```

### SEE ALSO

* [patchy](patchy.md)	 - Work with patchy security findings from the terminal

