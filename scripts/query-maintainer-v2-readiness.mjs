const worker = "dark-factory-control-plane";
const version = "84d224bd-e270-4ede-b0f0-b9217f9cd306";
const accountId = process.env.CLOUDFLARE_ACCOUNT_ID;
const token = process.env.CLOUDFLARE_API_TOKEN;

if (!accountId || !token) {
  throw new Error("readiness log query has no Cloudflare credential");
}

const response = await fetch(
  `https://api.cloudflare.com/client/v4/accounts/${accountId}/workers/observability/telemetry/query`,
  {
    method: "POST",
    headers: {
      authorization: `Bearer ${token}`,
      "content-type": "application/json",
    },
    body: JSON.stringify({
      queryId: "dark-factory-maintainer-v2-readiness",
      timeframe: {
        from: Date.now() - 48 * 60 * 60 * 1000,
        to: Date.now(),
      },
      view: "events",
      limit: 500,
      parameters: {
        datasets: [],
        filterCombination: "and",
        filters: [
          {
            kind: "filter",
            key: "$metadata.service",
            operation: "eq",
            type: "string",
            value: worker,
          },
          {
            kind: "filter",
            key: "$workers.scriptVersion.id",
            operation: "eq",
            type: "string",
            value: version,
          },
        ],
      },
    }),
  },
);

let envelope;
try {
  envelope = await response.json();
} catch {
  throw new Error(`Cloudflare log query returned HTTP ${response.status} without JSON`);
}

if (!response.ok || envelope.success !== true) {
  const messages = Array.isArray(envelope.errors)
    ? envelope.errors.map((error) => error?.message).filter(Boolean).join("; ")
    : "request rejected";
  throw new Error(`Cloudflare log query failed with HTTP ${response.status}: ${messages}`);
}

const allowed = [
  "readiness:",
  "journal:",
  "app jwt signing failed",
  "github request could not be built",
  "github request failed",
  "github rejected",
  "installation rejected on:",
];
const diagnostics = [];
for (const event of Array.isArray(envelope.result?.events) ? envelope.result.events : []) {
  const metadata = event?.$metadata;
  const message = metadata?.message;
  if (typeof message === "string" && allowed.some((prefix) => message.startsWith(prefix))) {
    diagnostics.push({ timestamp: event.timestamp, message });
  }
  if (typeof metadata?.error === "string") {
    diagnostics.push({ timestamp: event.timestamp, message: `error: ${metadata.error}` });
  }
}

diagnostics.sort((left, right) => left.timestamp - right.timestamp);
if (diagnostics.length === 0) {
  throw new Error(`no retained readiness diagnostics found for Worker version ${version}`);
}
for (const diagnostic of diagnostics) {
  console.log(JSON.stringify(diagnostic));
}
