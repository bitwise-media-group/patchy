# Local development on Colima

The [dev overlay](kustomize.md) assumes a local [kind](https://kind.sigs.k8s.io/) cluster, but
[Colima](https://github.com/abiosoft/colima) — a Lima VM running Docker with an optional embedded [k3s](https://k3s.io/)
— is a drop-in alternative on macOS (and Linux). The same overlay applies unchanged, and three things get simpler:

- **No image loading.** With Colima's default Docker runtime, k3s shares Docker's image store — anything you
  `docker build` or `docker tag` is immediately runnable in the cluster. The `kind load docker-image` step disappears.
- **No port-mapping config.** Colima forwards every listening TCP port in the VM to `127.0.0.1` on the host
  automatically, so the dev overlay's NodePorts (30079–30082) — and an ingress controller's 80/443 — appear on
  `localhost` without kind's `extraPortMappings`.
- **NetworkPolicy is enforced.** k3s embeds a network policy controller, so the base default-deny in `patchy-agents`
  actually applies — unlike kindnet, which accepts the policies and ignores them.

That last point is also why Colima supports the piece kind makes awkward: a real **Ingress in front of the
webhook-controller**, the same shape production uses, instead of a bare NodePort.

!!! tip "One command"

    The next three sections — start the cluster, build the images, apply the dev overlay — are wrapped in a single
    task: `mise run dev-colima` (or `make dev-colima`). Re-run it after code changes to rebuild the images and
    redeploy; it restarts the deployments so the fresh build actually rolls out. Override the VM size on first start
    with `COLIMA_CPU` / `COLIMA_MEMORY` (defaults 4 / 8), and pass a PAT with `GITHUB_TOKEN` so the controllers can
    actually reach GitHub — see [GitHub credentials](#github-credentials). It starts colima with the bundled Traefik
    enabled, so when it finishes the webhook is reachable through the
    [Ingress](#ingress-for-the-webhook-controller) at `http://localhost/webhook` as well as the NodePort at
    `http://localhost:30079`. The manual steps below remain the explanation of what it does.

## Start the cluster

```sh
brew install colima kubectl
colima start --kubernetes --cpu 4 --memory 8 --k3s-arg=--tls-san=localhost
```

`--kubernetes` boots k3s inside the VM (pin it with `--kubernetes-version`, which must match a k3s release tag) and
switches your Docker and kubeconfig contexts to `colima`. Two k3s defaults matter here:

- Colima's _default_ `--k3s-arg` is `--disable=traefik`, which would leave the bundled Traefik ingress controller off.
  Passing any explicit `--k3s-arg` replaces that default — the inert `--tls-san=localhost` above exists purely to
  displace it — so Traefik **is** running, and the [dev overlay's Ingress](#ingress-for-the-webhook-controller) works
  out of the box. (An instance started without this flag needs `colima stop` and a restart with it; colima persists the
  k3s args and reinstalls the cluster when they change.)
- k3s's `servicelb` (klipper-lb) **is** running: a `LoadBalancer` Service gets the VM's node address and binds its ports
  on the node, which is what lets Colima's port forwarding put an ingress controller on `localhost`.

## Build the images

`make snapshot` builds per-arch, unpushed `ghcr.io/…:v<next>-snapshot-<sha>-<arch>` images. Retag the host-arch ones
with the `patchy/<name>:dev` names the dev overlay expects — and that is the whole "load" step, because the Docker
runtime and k3s share one image store:

```sh
make snapshot

arch=arm64 # amd64 on Intel
for app in webhook-controller source-controller context-controller \
           remediation-controller agent-runner; do
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

Everything the [Kustomize page](kustomize.md) says about dev applies — placeholder Secrets, 2-minute windows, the fake
harness — and the NodePorts are reachable immediately, no cluster config needed:

```sh
curl -s http://localhost:30079/healthz   # webhook-controller, the routing entry point
```

At this point you can stop and use the kind flow verbatim: point a tunnel (`gh webhook forward`, smee.io) at
`http://localhost:30079/webhook`.

## GitHub credentials

The dev overlay ships **placeholder** GitHub App credentials (`app-id: "0"`), which fail the auth check at boot —
without real credentials the source, context, and remediation controllers crash-loop with
`github auth: set --github-token, or --github-app-id with --github-app-private-key-file`. The webhook-controller is
unaffected: it holds no GitHub credential by design.

The dev shortcut is a personal access token: `--github-token` [wins over App auth](../configuration/index.md), and the
dev overlay wires `PATCHY_GITHUB_TOKEN` into the three GitHub-facing controllers from an _optional_
`patchy-github-token` Secret. `dev-colima` creates that Secret for you when `GITHUB_TOKEN` is set:

```sh
GITHUB_TOKEN="$(gh auth token)" make dev-colima    # or export a PAT yourself
```

Re-running the task with a (new) `GITHUB_TOKEN` updates the Secret, and the rollout restart it already performs puts the
token into the pods. Without `GITHUB_TOKEN` the task prints a note and deploys anyway — pods start, GitHub auth fails,
and you can add the Secret later by re-running with the variable set (or by hand:
`kubectl -n patchy create secret generic patchy-github-token --from-literal=token=<pat>` followed by a
`kubectl -n patchy rollout restart deployment`).

To use real GitHub App credentials instead, overwrite `patchy-github-app` as described in
`deploy/kustomize/overlays/dev/secret-dev.yaml` — the token Secret simply stays absent.

## Ingress for the webhook-controller

The NodePort works, but Colima also runs the production shape: an ingress controller in front of the
`patchy-webhook-controller` Service, exposing only `/webhook`. With Traefik enabled at start (above), this is already
done — the **dev overlay ships a host-less, class-less Ingress** (`overlays/dev/ingress-webhook.yaml`) for exactly this.
Class-less on purpose: a dev cluster has one ingress controller, and the cluster's default IngressClass is assigned on
admission — k3s marks its bundled Traefik as the default, and on stock kind the object is simply inert.

The plumbing that makes it reach `localhost`: k3s's Traefik is a `LoadBalancer` Service, servicelb gives it the node's
address, and Colima forwards the listening 80/443 to `localhost` (macOS allows unprivileged binds below 1024, so no sudo
is involved).

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

Smoke-test the route. The webhook server registers `POST /webhook` only, so a GET answering **405** proves the request
reached the controller (a 404 means the Ingress didn't match):

```sh
curl -i http://localhost/webhook                      # dev-overlay Ingress
curl -i http://patchy.127.0.0.1.sslip.io/webhook      # chart Ingress
```

Finally, tunnel GitHub deliveries at the Ingress instead of the NodePort:

```sh
gh webhook forward --repo <owner>/<repo> \
  --events code_scanning_alert,issues,issue_comment,pull_request \
  --url http://localhost/webhook
```

TLS stays out of the local picture on purpose: GitHub's HTTPS leg ends at the tunnel, which re-delivers to the Ingress
over plain HTTP on your machine. If you want the cluster reachable from other devices instead of a tunnel, start Colima
with `--network-address` to give the VM a routable IP and point clients (and an sslip.io name built from that IP) at it.

!!! warning "Closer to production, still not a sandbox"

    k3s enforcing NetworkPolicy means the L3/L4 floor from the [isolation model](isolation.md) is real on Colima —
    an improvement over kind, where it silently does nothing. The FQDN layer still isn't there (no Cilium, no
    Istio), and the dev overlay ships throwaway credentials and the fake harness. Treat this as a faster inner
    loop, not a security-representative environment.
