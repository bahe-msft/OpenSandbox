export const meta = {
  name: "envoy-proxy-validation",
  description: "Central entrypoint for Envoy egress proxy validation: run fast checks, Docker smoke for Envoy/mitmproxy, and optional benchmark.",
};

// Usage examples:
//
// Default: fast checks + Envoy Docker smoke + mitmproxy Docker smoke baseline; skips performance.
// Docker smoke agent calls use smol-workflows sandbox isolation with exe-dev/default by default.
// Prerequisite for smoke steps: smol-sandbox-exe-dev on PATH and `ssh exe.dev ls --json` works.
//   smol-wf run .agents/workflows/envoy-proxy-validation.mjs --agent-provider pi
//
// Fast checks only: egress Go/Python tests plus focused server/K8s egress wiring tests.
//   smol-wf run .agents/workflows/envoy-proxy-validation.mjs --agent-provider pi \
//     --args-runDockerSmoke false --args-runPerformance false
//
// Docker smoke only: build the image by default inside exe-dev/default, then run Envoy and mitmproxy black-box smoke tests in parallel.
//   smol-wf run .agents/workflows/envoy-proxy-validation.mjs --agent-provider pi \
//     --args-runFast false --args-runDockerSmoke true
//
// Envoy smoke without mitmproxy baseline: useful after fast checks are already green.
//   smol-wf run .agents/workflows/envoy-proxy-validation.mjs --agent-provider pi \
//     --args-runMitmproxyBaseline false
//
// Include benchmark planning without running heavy performance tests.
//   smol-wf run .agents/workflows/envoy-proxy-validation.mjs --agent-provider pi \
//     --args-runPerformance true --args-benchmarkMode plan-only
//
// Include a bounded benchmark when Docker/network access are available in the exe-dev sandbox.
//   smol-wf run .agents/workflows/envoy-proxy-validation.mjs --agent-provider pi \
//     --args-runPerformance true --args-benchmarkMode quick
//
// Key args:
//   --args-runFast true|false              default true; invokes envoy-proxy-fast-checks.mjs
//   --args-runDockerSmoke true|false       default true; invokes Docker smoke workflow
//   --args-runMitmproxyBaseline true|false default true; runs mitmproxy smoke after Envoy smoke
//   --args-runPerformance true|false       default false; invokes envoy-proxy-benchmark.mjs
//   --args-benchmarkMode plan-only|quick|existing-script  default plan-only
//   --args-buildImage true|false           default true; Docker smoke builds opensandbox/egress:envoy-local
//   --args-runCredentialVault true|false   default true; smoke tests include Credential Vault injection
//   --args-sandboxProfile <profile|none>   default exe-dev/default for Docker smoke agent isolation
//   --args-envoyPolicyPort <port>          default 18080
//   --args-mitmproxyPolicyPort <port>      default 18081, so host-mode parallel smoke avoids port conflicts
//   --args-image <tag>                     default opensandbox/egress:envoy-local

const summarySchema = {
  type: "object",
  properties: {
    status: { type: "string", enum: ["passed", "failed", "blocked"] },
    summary: { type: "string" },
    parityFindings: {
      type: "array",
      items: {
        type: "object",
        properties: {
          area: { type: "string" },
          result: { type: "string" },
          notes: { type: "string" },
        },
        required: ["area", "result", "notes"],
      },
    },
    blockers: { type: "array", items: { type: "string" } },
    nextSteps: { type: "array", items: { type: "string" } },
  },
  required: ["status", "summary", "parityFindings", "blockers", "nextSteps"],
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

function resultStatus(result) {
  if (!result) return "blocked";
  if (typeof result.overallStatus === "string") return result.overallStatus;
  if (typeof result.status === "string") return result.status;
  return "blocked";
}

function combinedStatus(results) {
  const statuses = Object.values(results).map(resultStatus);
  if (statuses.includes("failed")) return "failed";
  if (statuses.includes("blocked")) return "blocked";
  return "passed";
}

function addIssue(issues, source, status, message, details = "") {
  issues.push({
    source,
    status,
    message: String(message || status),
    details: String(details || ""),
  });
}

function collectIssuesFromCheckArray(issues, source, checks) {
  for (const check of checks || []) {
    if (!check || typeof check !== "object") continue;
    const status = check.status || "unknown";
    if (!["passed", "skipped"].includes(status)) {
      addIssue(issues, `${source}.${check.name || "check"}`, status, check.name || "check failed", check.details || "");
    }
  }
}

function collectIssuesFromWorkflowResult(issues, source, result) {
  if (!result || typeof result !== "object") {
    addIssue(issues, source, "blocked", "missing workflow result");
    return;
  }

  const status = resultStatus(result);
  if (["failed", "blocked"].includes(status)) {
    addIssue(issues, source, status, `${source} ${status}`, result.summary?.summary || result.summary || "");
  }

  for (const failure of result.failures || []) {
    addIssue(issues, source, "failed", failure);
  }
  for (const blocker of result.blockers || []) {
    addIssue(issues, source, "blocked", blocker);
  }

  if (Array.isArray(result.checks)) {
    collectIssuesFromCheckArray(issues, `${source}.checks`, result.checks);
  } else if (result.checks && typeof result.checks === "object") {
    for (const [name, checkResult] of Object.entries(result.checks)) {
      collectIssuesFromWorkflowResult(issues, `${source}.checks.${name}`, checkResult);
    }
  }

  const summaryBlockers = result.summary?.blockers;
  if (Array.isArray(summaryBlockers)) {
    for (const blocker of summaryBlockers) {
      addIssue(issues, `${source}.summary`, "blocked", blocker);
    }
  }

  const parityFindings = result.summary?.parityFindings;
  if (Array.isArray(parityFindings)) {
    for (const finding of parityFindings) {
      const findingResult = String(finding?.result || "").toLowerCase();
      if (/(fail|block|gap|missing|risk|not pass|regress)/.test(findingResult)) {
        addIssue(
          issues,
          `${source}.summary.parityFindings.${finding?.area || "unknown"}`,
          findingResult.includes("block") ? "blocked" : "failed",
          finding?.area || "parity finding",
          finding?.notes || finding?.result || "",
        );
      }
    }
  }
}

function collectIssues(results) {
  const issues = [];
  for (const [source, result] of Object.entries(results)) {
    collectIssuesFromWorkflowResult(issues, source, result);
  }
  return issues;
}

export default async function run(input = {}) {
  const cfg = workflowArgs(input);
  const image = String(cfg.image || "opensandbox/egress:envoy-local");
  const runFast = boolArg(cfg.runFast, true);
  const runDockerSmoke = boolArg(cfg.runDockerSmoke, true);
  const runMitmproxyBaseline = boolArg(cfg.runMitmproxyBaseline, true);
  const runPerformance = boolArg(cfg.runPerformance, false);
  const buildImage = boolArg(cfg.buildImage, true);
  const cleanup = boolArg(cfg.cleanup, true);
  const runCredentialVault = boolArg(cfg.runCredentialVault, true);
  const sandboxProfile = cfg.sandboxProfile === undefined ? "exe-dev/default" : String(cfg.sandboxProfile);
  const benchmarkMode = String(cfg.benchmarkMode || "plan-only");

  const results = {};

  if (runFast) {
    phase("fast checks");
    results.fastChecks = await workflow(
      { scriptPath: "./envoy-proxy-fast-checks.mjs" },
      {
        includeServer: boolArg(cfg.includeServer, true),
        serverKeyword: cfg.serverKeyword || "egress or mitm or credential or envoy",
      },
    );
  }

  if (runDockerSmoke) {
    phase("docker smoke");
    const smokeTasks = [
      async () =>
        workflow(
          { scriptPath: "./envoy-proxy-docker-smoke.mjs" },
          {
            backend: "envoy",
            image,
            containerName: cfg.envoyContainerName || "egress-envoy-smoke",
            policyPort: cfg.envoyPolicyPort || 18080,
            token: cfg.token || "test-token",
            allowHost: cfg.allowHost || "example.com",
            denyHost: cfg.denyHost || "github.com",
            credentialHost: cfg.credentialHost || "httpbin.org",
            runCredentialVault,
            sandboxProfile,
            buildImage,
            cleanup,
          },
        ),
    ];

    if (runMitmproxyBaseline) {
      smokeTasks.push(async () =>
        workflow(
          { scriptPath: "./envoy-proxy-docker-smoke.mjs" },
          {
            backend: "mitmproxy",
            image,
            containerName: cfg.mitmproxyContainerName || "egress-mitmproxy-smoke",
            policyPort: cfg.mitmproxyPolicyPort || 18081,
            token: cfg.token || "test-token",
            allowHost: cfg.allowHost || "example.com",
            denyHost: cfg.denyHost || "github.com",
            credentialHost: cfg.credentialHost || "httpbin.org",
            runCredentialVault,
            sandboxProfile,
            buildImage,
            cleanup,
          },
        ),
      );
    }

    const smokeResults = await parallel(smokeTasks);
    results.envoySmoke = smokeResults[0];
    if (runMitmproxyBaseline) {
      results.mitmproxySmoke = smokeResults[1];
    }
  }

  if (runPerformance) {
    phase("performance validation");
    results.benchmark = await workflow(
      { scriptPath: "./envoy-proxy-benchmark.mjs" },
      {
        mode: benchmarkMode,
        image,
        sandboxProfile,
        buildImage,
        sampleSize: cfg.sampleSize || "20",
        scenarios: cfg.scenarios || "short,download",
      },
    );
  }

  const status = combinedStatus(results);
  const issues = collectIssues(results);

  phase("validation summary");
  const summary = await agent(
    `Summarize this Envoy egress proxy validation run.\n\n` +
      `Use the known parity risks from envoy-proxy-test.md: child process supervision, response-header redaction, ignore_hosts/SNI pass-through, runtime wiring, redirect scope, HTTP/2/upstream SNI/TLS.\n\n` +
      `Combined status computed by workflow: ${status}\n\n` +
      `Deterministically collected failures/issues JSON:\n${JSON.stringify(issues, null, 2)}\n\n` +
      `Raw child workflow results JSON:\n${JSON.stringify(results, null, 2)}\n\n` +
      `Return a concise structured summary with blockers and next steps.`,
    { phase: "validation summary", schema: summarySchema },
  );

  return {
    overallStatus: status,
    issues,
    summary,
    results,
  };
}
