export const meta = {
  name: "envoy-proxy-benchmark",
  description: "Run or prepare performance validation for Envoy versus mitmproxy egress transparent proxy backends, using exe-dev sandbox isolation for Docker-running modes by default.",
};

const benchmarkResultSchema = {
  type: "object",
  properties: {
    status: { type: "string", enum: ["passed", "failed", "blocked"] },
    mode: { type: "string" },
    summary: { type: "string" },
    commands: { type: "array", items: { type: "string" } },
    metrics: {
      type: "array",
      items: {
        type: "object",
        properties: {
          scenario: { type: "string" },
          backend: { type: "string" },
          metric: { type: "string" },
          value: { type: "string" },
          notes: { type: "string" },
        },
        required: ["scenario", "backend", "metric", "value", "notes"],
      },
    },
    failures: { type: "array", items: { type: "string" } },
    artifacts: { type: "array", items: { type: "string" } },
    recommendations: { type: "array", items: { type: "string" } },
    sandboxProfile: { type: "string" },
  },
  required: ["status", "mode", "summary", "commands", "metrics", "failures", "artifacts", "recommendations", "sandboxProfile"],
};

function workflowArgs(input) {
  if (input && typeof input === "object" && !Array.isArray(input)) return input;
  if (typeof args !== "undefined" && args && typeof args === "object") return args;
  return {};
}

function boolArg(value, defaultValue) {
  if (value === undefined || value === null || value === "") return defaultValue;
  return ["1", "true", "yes", "y", "on"].includes(String(value).toLowerCase());
}

export default async function run(input = {}) {
  const cfg = workflowArgs(input);
  const mode = String(cfg.mode || "quick").toLowerCase();
  if (!["quick", "existing-script", "plan-only"].includes(mode)) {
    throw new Error(`mode must be quick, existing-script, or plan-only; got ${mode}`);
  }

  const image = String(cfg.image || "opensandbox/egress:envoy-local");
  const buildImage = boolArg(cfg.buildImage, true);
  const sampleSize = String(cfg.sampleSize || "20");
  const scenarios = String(cfg.scenarios || "short,download");
  const rawSandboxProfile = cfg.sandboxProfile === undefined ? "exe-dev/default" : String(cfg.sandboxProfile);
  const sandboxProfile = ["", "none", "host", "false"].includes(rawSandboxProfile.toLowerCase())
    ? null
    : rawSandboxProfile;
  const agentOptions = { phase: `benchmark ${mode}`, schema: benchmarkResultSchema };
  if (sandboxProfile && mode !== "plan-only") {
    agentOptions.isolation = { type: "sandbox", profile: sandboxProfile };
  }

  phase(`benchmark ${mode}`);

  return agent(
    `You are validating performance for the OpenSandbox Envoy egress transparent proxy backend.\n\n` +
      `Repository root is the current working directory. Avoid editing repository files. If you need a temporary script, place it under /tmp and report its path.\n\n` +
      `Configuration:\n` +
      `- mode: ${mode}\n` +
      `- sandboxProfile: ${mode === "plan-only" ? "none (plan-only does not need sandbox)" : sandboxProfile || "none (host execution)"}\n` +
      `- image: ${image}\n` +
      `- buildImage: ${buildImage}\n` +
      `- sampleSize: ${sampleSize}\n` +
      `- scenarios: ${scenarios}\n\n` +
      `If mode=plan-only:\n` +
      `- Do not run benchmarks. Inspect components/egress/tests/bench-mitm-overhead.sh and envoy-proxy-test.md.\n` +
      `- Report the exact commands and script changes needed to add an Envoy phase. status=passed if the plan is clear.\n\n` +
      `If mode=existing-script:\n` +
      (sandboxProfile
        ? `- This agent call is isolated with smol-workflows sandbox profile ${sandboxProfile}; run Docker commands inside that exe.dev VM. If Docker is unavailable there, status=blocked.\n`
        : `- This agent call is not sandbox-isolated; run Docker commands in the host workspace.\n`) +
      `- Run the existing benchmark script without modifying it.\n` +
      `- If buildImage=true, let the script build; otherwise use SKIP_BUILD=1 IMG=${image} where supported.\n` +
      `- Suggested command: cd components/egress && BENCH_SAMPLE_SIZE=${sampleSize} BENCH_SCENARIOS=${JSON.stringify(scenarios)} ./tests/bench-mitm-overhead.sh\n` +
      `- Report that this compares dns+nft versus dns+nft+mitmproxy only unless you find the script already supports Envoy.\n\n` +
      `If mode=quick:\n` +
      (sandboxProfile
        ? `- This agent call is isolated with smol-workflows sandbox profile ${sandboxProfile}; run Docker commands inside that exe.dev VM. If Docker is unavailable there, status=blocked.\n`
        : `- This agent call is not sandbox-isolated; run Docker commands in the host workspace.\n`) +
      `- Run a short, bounded comparison that includes Envoy and mitmproxy. It is acceptable to use /tmp helper scripts and repeated curl -w time_total loops.\n` +
      `- Build ${image} first if buildImage=true.\n` +
      `- Compare at least: dns+nft without transparent MITM, dns+nft+mitmproxy, dns+nft+envoy.\n` +
      `- Use a small allowed HTTPS target such as example.com unless network access is unavailable.\n` +
      `- Collect at minimum: request count, average time_total, p50, p99 if possible, wall time, and docker stats snapshot or CPU/RSS notes.\n` +
      `- Keep runtime short. If Docker or network access is unavailable, status=blocked.\n\n` +
      `For all modes: include every command you actually ran, artifact paths under /tmp, failures, concrete recommendations, and sandboxProfile=${mode === "plan-only" ? "none" : sandboxProfile || "none"}.`,
    agentOptions,
  );
}
