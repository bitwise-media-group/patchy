## patchy completion bash

Generate the autocompletion script for bash

### Synopsis

Generate the autocompletion script for the bash shell.

This script depends on the 'bash-completion' package.
If it is not installed already, you can install it via your OS's package manager.

To load completions in your current shell session:

	source <(patchy completion bash)

To load completions for every new session, execute once:

#### Linux:

	patchy completion bash > /etc/bash_completion.d/patchy

#### macOS:

	patchy completion bash > $(brew --prefix)/etc/bash_completion.d/patchy

You will need to start a new shell for this setup to take effect.


```
patchy completion bash
```

### Options

```
  -h, --help              help for bash
      --no-descriptions   disable completion descriptions
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

* [patchy completion](patchy_completion.md)	 - Generate the autocompletion script for the specified shell

