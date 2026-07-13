# Kustomize

The Helm chart is the primary deployment surface, but the same stack — identical resources, defaults, and isolation
model — renders from the kustomize tree in `deploy/`. Use it when your platform standardises on kustomize or you want
overlay-style patching; `deploy/README.md` in the repository is the full operator document.

```text
deploy/
├── kustomize/
│   ├── base/                  # namespaces, serviceaccounts, RBAC, configmap,
│   │                          #   the three Deployments, Services, network policies
│   ├── components/cilium/     # optional FQDN egress (CiliumNetworkPolicy)
│   ├── components/istio/      # optional Sidecar + ServiceEntry + netpol
│   └── overlays/
│       ├── dev/               # kind: NodePorts, throwaway secrets, 2m windows, fake harness
│       └── prod/              # digest-pinned images + the cilium component
└── README.md
```

Apply an overlay (render first with `kubectl kustomize` if you want to review):

```sh
kubectl apply -k deploy/kustomize/overlays/dev
kubectl apply -k deploy/kustomize/overlays/prod
```

- **dev** targets a local kind cluster: NodePort services (30080/30081/30082), placeholder Secrets, fast loops (2-minute
  accumulation and min-age, 10s reconcile), and the `fake` harness so no tokens are spent. Tunnel GitHub deliveries in
  with `gh webhook forward` or smee.io. Remember that kind's default CNI ignores NetworkPolicy.
- **prod** pins every image by digest (the checked-in `sha256:0000…` values are placeholders — replace them with the
  published digests, including the `PATCHY_AGENT_IMAGE` reference inside the ConfigMap patch) and layers the Cilium
  component for FQDN egress.

The same [three Secrets](../getting-started/install.md#create-the-secrets) are required either way — the base only ships
a documentation-only `secrets.example.yaml`, and the dev overlay's throwaway values exist so the pods schedule, not so
the pipeline works.
