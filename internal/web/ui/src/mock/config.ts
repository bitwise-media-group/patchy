// Demo configuration dataset — what GET /api/config would project for a
// small estate. Deep-cloned per call so demo backfills can mutate freely.

import type { ConfigDataset } from "../types";

const config: ConfigDataset = {
  generatedAt: "2026-07-21T08:45:00Z",
  namespace: "patchy",
  forges: [
    {
      name: "github",
      provider: "github",
      orgs: ["acme"],
      secretRef: "forge-github",
      interval: "10m0s",
      ready: "True",
    },
  ],
  integrations: [
    {
      name: "gh",
      provider: "github",
      webhookPath: "/github/webhooks",
      secretRef: "integration-github",
      interval: "10m0s",
      ready: "True",
      capabilities: ["issues", "codeScanningAlerts", "redelivery"],
      redelivery: {
        lastSweepAt: "2026-07-21T08:40:00Z",
        scanned: 112,
        redelivered: 2,
      },
      backfill: {
        lastRunAt: "2026-07-20T16:02:00Z",
        listed: 214,
        ingested: 214,
        requestedBy: "op@acme.test",
        requestedAt: "2026-07-20T16:01:30Z",
      },
      backfillSupported: true,
    },
    {
      name: "gcp",
      provider: "google-cloud",
      webhookPath: "/google-cloud/webhooks",
      interval: "10m0s",
      ready: "True",
      capabilities: ["securityCommandCenter", "cloudAssetInventory"],
    },
    {
      name: "warehouse",
      provider: "generic",
      webhookPath: "/generic/warehouse/webhooks",
      secretRef: "integration-warehouse",
      interval: "10m0s",
      ready: "True",
      capabilities: ["source", "enhance"],
    },
  ],
  enhancers: [
    { id: "google-cloud-labels", integration: "gcp", enabled: true },
    { id: "generic", integration: "warehouse", enabled: true },
  ],
};

export function mockConfig(): ConfigDataset {
  return JSON.parse(JSON.stringify(config)) as ConfigDataset;
}
