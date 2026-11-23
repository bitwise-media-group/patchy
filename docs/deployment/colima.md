# Local development on Colima

The [dev overlay](kustomize.md) assumes a local [kind](https://kind.sigs.k8s.io/) cluster. On macOS (and Linux) you can
use [Colima](https://github.com/abiosoft/colima) instead. Colima is a Lima VM that runs Docker, with an optional
embedded [k3s](https://k3s.io/) cluster. The same overlay applies unchanged, and three things get simpler:

- **No image loading.** With Colima's default Docker runtime, k3s shares Docker's image store. Anything you
  `docker build` or `docker tag` can run in the cluster straight away, so you skip the `kind load docker-image` step.
- **No port-mapping config.** Colima forwards every listening TCP port in the VM to `127.0.0.1` on the host
  automatically. The dev overlay's webhook NodePort (30079) appears on `localhost` without kind's `extraPortMappings`.
  (There is one intended exception: with a reachable VM address and Traefik enabled, colima does not forward 80/443. See
  [Ingress](#ingress-for-the-integration-controller).)
- **NetworkPolicy is enforced.** k3s embeds a network policy controller, so the base default-deny in `patchy-agents`
  applies. kindnet, by contrast, accepts the policies and ignores them.

That last point is also why Colima supports something kind makes awkward: a real **Ingress in front of the
integration-controller**, the same shape production uses, instead of a bare NodePort.

!!! tip "One command"

    One task runs the next three sections (start the cluster, build the images, apply the dev overlay):
    `mise run dev-colima` (or `make dev-colima`). Re-run it after code changes to rebuild the images and redeploy. It
    restarts the deployments so the fresh build rolls out.

    - Override the VM size on first start with `COLIMA_CPU` / `COLIMA_MEMORY` (defaults 4 / 8).
    - Pass a PAT with `GITHUB_TOKEN` so the controllers can reach GitHub. See [GitHub credentials](#github-credentials).

    The task starts colima with the bundled Traefik enabled. When it finishes, it probes the webhook
    [Ingress](#ingress-for-the-integration-controller) and prints its URL: `http://localhost/github/webhooks`, or
    `http://<vm-ip>/github/webhooks` when colima runs with a reachable network address. It also prints the NodePort at
    `http://localhost:30079`. The manual steps below explain what the task does.

## Start the cluster

```sh
brew install colima kubectl
colima start --kubernetes --cpu 4 --memory 8 --k3s-arg=--tls-san=localhost
```

`--kubernetes` boots k3s inside the VM and switches your Docker and kubeconfig contexts to `colima`. You can pin the
version with `--kubernetes-version`, which must match a k3s release tag. Two k3s defaults matter here:

- Colima's _default_ `--k3s-arg` is `--disable=traefik`, which would leave the bundled Traefik ingress controller off.
  Passing any explicit `--k3s-arg` replaces that default. The `--tls-san=localhost` above does nothing else; it is there
  only to replace the default. So Traefik **is** running, and the
  [dev overlay's Ingress](#ingress-for-the-integration-controller) works by default. (If you started an instance without
  this flag, run `colima stop` and start it again with the flag. Colima saves the k3s args and reinstalls the cluster
  when they change.)
- k3s's `servicelb` (klipper-lb) **is** running. A `LoadBalancer` Service gets the VM's node address and binds its ports
  on the node. This is what lets Colima's port forwarding put an ingress controller on `localhost`.

## Build the images

`make snapshot` builds per-arch, unpushed `ghcr.io/…:v<next>-snapshot-<sha>-<arch>` images. Retag the host-arch ones
with the `patchy/<name>:dev` names the dev overlay expects. That retag is the only "load" step you need, because the
Docker runtime and k3s share one image store:

```sh
make snapshot

arch=arm64 # amd64 on Intel
for app in integration-controller source-controller context-controller \
           investigation-controller remediation-controller egress-broker \
           claude-agent-runner codex-agent-runner status-server; do
  tag=$(docker images "ghcr.io/bitwise-media-group/patchy/$app" \
    --format '{{.Tag}}' | grep -- "-$arch$" | head -1)
  docker tag "ghcr.io/bitwise-media-group/patchy/$app:$tag" "patchy/$app:dev"
done
```

The `dev` tag never goes to a registry. Because it is not `:latest`, the pull policy defaults to `IfNotPresent` and k3s
runs the local image as it is.

!!! note "Containerd runtime"

    If you started Colima with `--runtime containerd`, Kubernetes sees only the images in containerd's `k8s.io`
    namespace. Build or import with `nerdctl --namespace k8s.io`. The Docker runtime avoids this extra step.

## Apply the dev overlay

```sh
kubectl apply -k deploy/kustomize/overlays/dev
```

Everything the [Kustomize page](kustomize.md) says about dev applies here: placeholder Secrets and `Integration`/`Forge`
CRs, 2-minute windows, and the fake harness. The NodePort is reachable straight away, with no cluster config:

```sh
curl -s http://localhost:30079/healthz   # the integration-controller's webhook listener
```

From here you can follow the kind flow word for word. You have two options:

- Point a tunnel (`gh webhook forward`, smee.io) at `http://localhost:30079/github/webhooks`.
- Skip GitHub and replay recorded fixtures. `mise run replay` signs and delivers to that same URL by default. The
  `-dev-secret` flag signs with the dev overlay's placeholder webhook secret (`dev-webhook-secret-replace-me`), so you
  do not need to export a secret. The task runs in `e2e/`, so fixture paths are relative to that directory:

```sh
mise run replay -- -dev-secret fixtures/webhooks/code_scanning_alert.created.json
```

(If you are replaying against a stack with a real webhook secret, use `-secret-file <file>` instead.) With the
placeholder GitHub credential, the projection and artifact calls fail. Ingestion and the full CR state machine still
run, and `kubectl get findings -w` shows the pipeline moving.

## GitHub credentials

The dev overlay ships a **placeholder** `patchy-github` Secret (`token: dev-not-a-real-token`). With it, the CRs
validate and the receivers start, but every real GitHub call fails until you replace it. The quickest dev option is a
personal access token under the Secret's `token` key
([it wins over App auth](../getting-started/install.md#create-the-secrets)). `dev-colima` overwrites the Secret for you
when `GITHUB_TOKEN` is set:

```sh
GITHUB_TOKEN="$(gh auth token)" make dev-colima    # or export a PAT yourself
```

Re-running the task with a new `GITHUB_TOKEN` updates the Secret. The controllers read the Secret on demand through the
API, so you do not strictly need a rollout (the task restarts the deployments anyway).

Without `GITHUB_TOKEN`, the task prints a note and deploys anyway. Pods start and ingestion works, but GitHub calls
fail. You can add the token later by re-running the task with the variable set, or by hand:

```sh
kubectl -n patchy create secret generic patchy-github \
  --from-literal=token=<pat> \
  --from-literal=webhookSecret=dev-webhook-secret-replace-me \
  --dry-run=client -o yaml | kubectl apply -f -
```

To use real GitHub App credentials instead, write `appID` + `privateKey` keys into the same Secret, as described in
`deploy/kustomize/overlays/dev/secret-dev.yaml`.

## Credential-less end to end

You can watch **every custom resource** progress without a GitHub token _or_ a model key. The `dev-fake` overlay
replaces GitHub with the e2e suite's in-memory API (run in-cluster) and replaces the model with a scripted agent. Run
`PATCHY_OVERLAY=dev-fake make dev-colima` to build and deploy the fakes alongside everything above. The full walkthrough
is on [Demo mode](dev-fake.md#credential-less-end-to-end-the-dev-fake-overlay).

## Ingress for the integration-controller

The NodePort works, but Colima can also run the production shape: an ingress controller in front of the
`patchy-integration-controller` Service, exposing only `/github/webhooks`. If you enabled Traefik at start (above), this
is already set up. The **dev overlay ships a host-less, class-less Ingress** (`overlays/dev/ingress-integration.yaml`)
for this purpose. It has no class because a dev cluster has one ingress controller, and Kubernetes assigns the cluster's
default IngressClass on admission. k3s marks its bundled Traefik as the default. On stock kind, the object does nothing.

Where the Ingress answers depends on colima's network mode. servicelb gives Traefik's `LoadBalancer` Service the node's
address. Then:

- **No reachable VM address** (colima's default, and how `dev-colima` starts a fresh instance): lima forwards the
  listening ports 80/443 to `localhost`. macOS allows unprivileged binds below 1024, so you do not need sudo. The URL is
  `http://localhost/github/webhooks`.
- **`network.address: true` / `--network-address`**: colima does **not** forward 80/443. This guard stops a VM that has
  its own IP from taking the host's web ports. Traefik answers at the VM's address instead:
  `http://<vm-ip>/github/webhooks`. `colima ls` prints the address, and `patchy.<vm-ip>.sslip.io` gives it a name.

`dev-colima` detects the mode, probes the route, and prints the working URL when it finishes.

Prefer ingress-nginx? Install it as the default class and it serves the same Ingress without Traefik:

```sh
helm upgrade --install ingress-nginx ingress-nginx \
  --repo https://kubernetes.github.io/ingress-nginx \
  --namespace ingress-nginx --create-namespace \
  --set controller.ingressClassResource.default=true
```

With the **Helm chart**, use the chart's own Ingress settings instead. `webhook.host` is required. An
[sslip.io](https://sslip.io) name resolves to `127.0.0.1`, so you do not need to edit `/etc/hosts`:

```yaml
webhook:
  host: patchy.127.0.0.1.sslip.io
  ingress:
    enabled: true
    className: nginx
    # no tls locally: the tunnel below terminates GitHub's HTTPS leg
```

Smoke-test the route. The webhook server registers `POST /github/webhooks` only. A GET that returns **405** proves the
request reached the controller. A 404 means the Ingress did not match:

```sh
curl -i http://localhost/github/webhooks                 # dev-overlay Ingress (VM IP instead of
                                                         # localhost with a network address)
curl -i http://patchy.127.0.0.1.sslip.io/github/webhooks # chart Ingress
```

Finally, point the GitHub delivery tunnel at the Ingress instead of the NodePort:

```sh
gh webhook forward --repo <owner>/<repo> \
  --events code_scanning_alert,secret_scanning_alert,issues,issue_comment,pull_request \
  --url http://localhost/github/webhooks
```

Local setups leave out TLS on purpose. GitHub's HTTPS leg ends at the tunnel, which re-delivers to the Ingress over
plain HTTP on your machine. If you want other devices to reach the cluster without a tunnel, start Colima with
`--network-address`. This gives the VM a routable IP. Point clients (and an sslip.io name built from that IP) at it.

!!! warning "Closer to production, still not a sandbox"

    Because k3s enforces NetworkPolicy, the L3/L4 floor from the [isolation model](../concepts/isolation.md) is real on
    Colima. That is an improvement over kind, where it silently does nothing. The FQDN layer is still missing (no
    Cilium, no Istio), and the dev overlay ships throwaway credentials and the fake harness. Treat this as a faster
    inner loop. It is not a security-representative environment.
