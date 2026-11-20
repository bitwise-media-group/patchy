## patchy review

Read an agent's report on a finding

### Synopsis

Read what the agent concluded: its ratings, its verdict, and the report it wrote.

On a terminal the report is rendered for reading. Piped, or with -o markdown, it
is printed as the markdown the agent wrote, so you can paste it into an issue
without losing anything.

--transcript prints the agent's conversation, --patch the remediation as a
`git am`-able patch, and --report-full the untruncated report. When the run was
archived these read the archive bucket directly with your own cloud credentials
(the AWS default chain or Google ADC), so you need read access to it; a
transcript that was not archived is read from the cluster instead. Add
--print-url to print a short-lived presigned URL rather than the content, and
--raw to get the transcript as JSONL.

```
patchy review <resource> [name] [flags]
```

### Examples

```
  patchy review finding my-finding
  patchy review investigation --finding my-finding
  patchy review investigation --finding my-finding --attempt 2
  patchy review remediation my-finding-rem-1 --web
  patchy review finding my-finding -o markdown > report.md
  patchy review investigation --finding my-finding --transcript | less
  patchy review remediation --finding my-finding --patch | git am
  patchy review remediation --finding my-finding --patch --print-url
```

### Options

```
      --attempt int32              which attempt to read (default: the latest)
      --finding string             select the run by its finding instead of by name
  -h, --help[=true|false]          help for review
      --patch[=true|false]         print the remediation as a git am-able patch (archived runs only)
      --print-url[=true|false]     print that URL instead of opening it (with --transcript/--patch/--report-full: a presigned archive URL)
      --raw[=true|false]           keep the machine frontmatter the agent emitted
      --report-full[=true|false]   print the untruncated report (archived runs only)
      --transcript[=true|false]    print the agent's conversation (--raw: as JSONL)
      --web[=true|false]           open the tracking issue (investigations) or pull request (remediations)
```

### Options inherited from parent commands

```
  -A, --all-namespaces[=true|false]   work across every namespace
      --context string                kubeconfig context to use
      --kubeconfig string             path to the kubeconfig file
  -n, --namespace string              namespace to work in (default: the context's)
      --no-color[=true|false]         disable colour and styling
  -o, --output string                 output format: table, wide, json, yaml, name, or markdown (default "table")
      --request-timeout duration      timeout for a single API call (default 30s)
  -v, --verbose[=true|false]          log what the CLI is doing to stderr
```

### SEE ALSO

* [patchy](patchy.md)	 - Work with patchy security findings from the terminal

