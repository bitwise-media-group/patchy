# Local development on Colima

The [dev overlay](kustomize.md) assumes a local [kind](https://kind.sigs.k8s.io/) cluster, but
[Colima](https://github.com/abiosoft/colima) — a Lima VM running Docker with an optional embedded [k3s](https://k3s.io/)
— is a drop-in alternative on macOS (and Linux). The same overlay applies unchanged, and three things get simpler:

- **No image loading.** With Colima's default Docker runtime, k3s shares Docker's image store — anything you
  `docker build` or `docker tag` is immediately runnable in the cluster. The `kind load docker-image` step disappears.
- **No port-mapping config.** Colima forwards every listening TCP port in the VM to `127.0.0.1` on the host
  automatically, so the dev overlay's webhook NodePort (30079) appears on `localhost` without kind's
  `extraPortMappings`. (One deliberate exception: with a reachable VM address and Traefik enabled, colima does not
  forward 80/443 — see [Ingress](#ingress-for-the-integration-controller).)
- **NetworkPolicy is enforced.** k3s embeds a network policy controller, so the base default-deny in `patchy-agents`
  actually applies — unlike kindnet, which accepts the policies and ignores them.

That last point is also why Colima supports the piece kind makes awkward: a real **Ingress in front of the
integration-controller**, the same shape production uses, instead of a bare NodePort.

!!! tip "One command"

    The next three sections — start the cluster, build the images, apply the dev overlay — are wrapped in a single
    task: `mise run dev-colima` (or `make dev-colima`). Re-run it after code changes to rebuild the images and
    redeploy; it restarts the deployments so the fresh build actually rolls out. Override the VM size on first start
    with `COLIMA_CPU` / `COLIMA_MEMORY` (defaults 4 / 8), and pass a PAT with `GITHUB_TOKEN` so the controllers can
    actually reach GitHub — see [GitHub credentials](#github-credentials). It starts colima with the bundled Traefik
    enabled and finishes by probing the webhook [Ingress](#ingress-for-the-integration-controller) and printing its
    URL — `http://localhost/github/webhooks`, or `http://<vm-ip>/github/webhooks` when colima runs with a reachable
    network address — alongside the NodePort at `http://localhost:30079`. The manual steps below remain the
    explanation of what it does.

## Start the cluster

```sh
brew install colima kubectl
colima start --kubernetes --cpu 4 --memory 8 --k3s-arg=--tls-san=localhost
```

`--kubernetes` boots k3s inside the VM (pin it with `--kubernetes-version`, which must match a k3s release tag) and
switches your Docker and kubeconfig contexts to `colima`. Two k3s defaults matter here:

- Colima's _default_ `--k3s-arg` is `--disable=traefik`, which would leave the bundled Traefik ingress controller off.
  Passing any explicit `--k3s-arg` replaces that default — the inert `--tls-san=localhost` above exists purely to
  displace it — so Traefik **is** running, and the [dev overlay's Ingress](#ingress-for-the-integration-controller)
  works out of the box. (An instance started without this flag needs `colima stop` and a restart with it; colima
  persists the k3s args and reinstalls the cluster when they change.)
- k3s's `servicelb` (klipper-lb) **is** running: a `LoadBalancer` Service gets the VM's node address and binds its ports
  on the node, which is what lets Colima's port forwarding put an ingress controller on `localhost`.

## Build the images

`make snapshot` builds per-arch, unpushed `ghcr.io/…:v<next>-snapshot-<sha>-<arch>` images. Retag the host-arch ones
with the `patchy/<name>:dev` names the dev overlay expects — and that is the whole "load" step, because the Docker
runtime and k3s share one image store:

```sh
make snapshot

arch=arm64 # amd64 on Intel
for app in integration-controller source-controller context-controller \
           investigation-controller remediation-controller agent-runner; do
  tag=$(docker images "ghcr.io/bitwise-media-group/patchy/$app" \
    --format '{{.Tag}}' | grep -- "-$arch$" | head -1)
  docker tag "ghcr.io/bitwise-media-group/patchy/$app:$tag" "patchy/$app:dev"
done
```

The `dev` tag never hits a registry: it is not `:latest`, so the pull policy defaults to `IfNotPresent` and k3s runs the
local image as-is.

!!! note "Containerd runtime"

    If you started Colima with `--runtime containerd`, only images in containerd's `k8s.io` namespace are visible to
    Kubernetes — build or import with `nerdctl --namespace k8s.io`. The Docker runtime avoids the extra step.

## Apply the dev overlay

```sh
kubectl apply -k deploy/kustomize/overlays/dev
```

Everything the [Kustomize page](kustomize.md) says about dev applies — placeholder Secrets and `Integration`/`Forge`
CRs, 2-minute windows, the fake harness — and the NodePort is reachable immediately, no cluster config needed:

```sh
curl -s http://localhost:30079/healthz   # the integration-controller's webhook listener
```

At this point you can stop and use the kind flow verbatim: point a tunnel (`gh webhook forward`, smee.io) at
`http://localhost:30079/github/webhooks`, or skip GitHub entirely and replay recorded fixtures — `mise run replay` signs
and delivers to that same URL by default, and `-dev-secret` signs with the dev overlay's placeholder webhook secret
(`dev-webhook-secret-replace-me`), so no secret needs exporting (the task runs in `e2e/`, so fixture paths are relative
to it):

```sh
mise run replay -- -dev-secret fixtures/webhooks/code_scanning_alert.created.json
```

(Replaying against a stack with a real webhook secret? `-secret-file <file>` instead.) With the placeholder GitHub
credential the projection and artifact calls fail, but ingestion and the whole CR state machine run:
`kubectl get findings -w` shows the pipeline moving.

## GitHub credentials

The dev overlay ships a **placeholder** `patchy-github` Secret (`token: dev-not-a-real-token`), so the CRs validate, the
receivers start — and every actual GitHub call fails until it is replaced. The dev shortcut is a personal access token
under the Secret's `token` key ([it wins over App auth](../getting-started/install.md#create-the-secrets)). `dev-colima`
overwrites the Secret for you when `GITHUB_TOKEN` is set:

```sh
GITHUB_TOKEN="$(gh auth token)" make dev-colima    # or export a PAT yourself
```

Re-running the task with a (new) `GITHUB_TOKEN` updates the Secret; the controllers read it on demand through the API,
so no rollout is strictly needed (the task restarts the deployments anyway). Without `GITHUB_TOKEN` the task prints a
note and deploys regardless — pods start, ingestion works, GitHub calls fail — and you can add the token later by
re-running with the variable set, or by hand:

```sh
kubectl -n patchy create secret generic patchy-github \
  --from-literal=token=<pat> \
  --from-literal=webhookSecret=dev-webhook-secret-replace-me \
  --dry-run=client -o yaml | kubectl apply -f -
```

To use real GitHub App credentials instead, write `appID` + `privateKey` keys into the same Secret, as described in
`deploy/kustomize/overlays/dev/secret-dev.yaml`.

## Credential-less end to end

To watch **every custom resource** progress without a GitHub token _or_ a model key, the `dev-fake` overlay replaces
GitHub with the e2e suite's in-memory API (run in-cluster) and the model with a scripted agent —
`PATCHY_OVERLAY=dev-fake make dev-colima` builds and deploys the fakes alongside everything above. The full walkthrough
is on [Demo tooling](dev-fake.md#credential-less-end-to-end-the-dev-fake-overlay).

## Ingress for the integration-controller

The NodePort works, but Colima also runs the production shape: an ingress controller in front of the
`patchy-integration-controller` Service, exposing only `/github/webhooks`. With Traefik enabled at start (above), this
is already done — the **dev overlay ships a host-less, class-less Ingress** (`overlays/dev/ingress-integration.yaml`)
for exactly this. Class-less on purpose: a dev cluster has one ingress controller, and the cluster's default
IngressClass is assigned on admission — k3s marks its bundled Traefik as the default, and on stock kind the object is
simply inert.

Where it answers depends on colima's network mode. servicelb gives Traefik's `LoadBalancer` Service the node's address,
and then:

- **No reachable VM address** (colima's default, and how `dev-colima` starts a fresh instance): lima forwards the
  listening 80/443 to `localhost` (macOS allows unprivileged binds below 1024, so no sudo is involved) —
  `http://localhost/github/webhooks`.
- **`network.address: true` / `--network-address`**: colima deliberately does **not** forward 80/443 — the guard keeps a
  VM that has its own IP from occupying the host's web ports — and Traefik answers at the VM's address instead:
  `http://<vm-ip>/github/webhooks` (`colima ls` prints the address; `patchy.<vm-ip>.sslip.io` gives it a name).

`dev-colima` detects the mode, probes the route, and prints the working URL when it finishes.

Prefer ingress-nginx? Install it as the default class and the same Ingress is satisfied without Traefik:

```sh
helm upgrade --install ingress-nginx ingress-nginx \
  --repo https://kubernetes.github.io/ingress-nginx \
  --namespace ingress-nginx --create-namespace \
  --set controller.ingressClassResource.default=true
```

With the **Helm chart**, use the built-in flavour instead — `webhook.host` is required, and an
[sslip.io](https://sslip.io) name resolves to `127.0.0.1` without touching `/etc/hosts`:

```yaml
webhook:
  host: patchy.127.0.0.1.sslip.io
  ingress:
    enabled: true
    className: nginx
    # no tls locally — the tunnel below terminates GitHub's HTTPS leg
```

Smoke-test the route. The webhook server registers `POST /github/webhooks` only, so a GET answering **405** proves the
request reached the controller (a 404 means the Ingress didn't match):

```sh
curl -i http://localhost/github/webhooks                 # dev-overlay Ingress (VM IP instead of
                                                         # localhost with a network address)
curl -i http://patchy.127.0.0.1.sslip.io/github/webhooks # chart Ingress
```

Finally, tunnel GitHub deliveries at the Ingress instead of the NodePort:

```sh
gh webhook forward --repo <owner>/<repo> \
  --events code_scanning_alert,issues,issue_comment,pull_request \
  --url http://localhost/github/webhooks
```

TLS stays out of the local picture on purpose: GitHub's HTTPS leg ends at the tunnel, which re-delivers to the Ingress
over plain HTTP on your machine. If you want the cluster reachable from other devices instead of a tunnel, start Colima
with `--network-address` to give the VM a routable IP and point clients (and an sslip.io name built from that IP) at it.

!!! warning "Closer to production, still not a sandbox"

    k3s enforcing NetworkPolicy means the L3/L4 floor from the [isolation model](isolation.md) is real on Colima —
    an improvement over kind, where it silently does nothing. The FQDN layer still isn't there (no Cilium, no
    Istio), and the dev overlay ships throwaway credentials and the fake harness. Treat this as a faster inner
    loop, not a security-representative environment.
