export const meta = {
  name: "envoy-proxy-fast-checks",
  description: "Run fast local correctness checks for the Envoy egress proxy PR: egress Go/Python tests plus server K8s wiring tests.",
};

const checkResultSchema = {
  type: "object",
  properties: {
    status: { type: "string", enum: ["passed", "failed", "blocked"] },
    summary: { type: "string" },
    commands: { type: "array", items: { type: "string" } },
    failures: { type: "array", items: { type: "string" } },
    artifacts: { type: "array", items: { type: "string" } },
  },
  required: ["status", "summary", "commands", "failures", "artifacts"],
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

function overallStatus(results) {
  const present = results.filter(Boolean);
  if (present.some((r) => r.status === "failed")) return "failed";
  if (present.some((r) => r.status === "blocked")) return "blocked";
  return "passed";
}

export default async function run(input = {}) {
  const cfg = workflowArgs(input);
  const includeServer = boolArg(cfg.includeServer, true);
  const serverKeyword = String(cfg.serverKeyword || "egress or mitm or credential or envoy");

  phase("fast correctness checks");

  const tasks = [
    async () =>
      agent(
        `You are running fast OpenSandbox egress component checks for the Envoy transparent proxy PR.\n\n` +
          `Repository root is the current working directory. Do not edit files. Run these commands and classify the result:\n\n` +
          `1. cd components/egress && go test ./...\n` +
          `2. cd components/egress && python3 -m unittest tests/test_mitmscripts_system.py\n\n` +
          `If a tool or dependency is missing, report status=blocked and include the exact blocker. ` +
          `If tests fail, report status=failed with the failing package/test names and relevant stderr. ` +
          `Include every command you actually ran.`,
        { phase: "egress unit tests", schema: checkResultSchema },
      ),
  ];

  if (includeServer) {
    tasks.push(async () =>
      agent(
        `You are running focused server/Kubernetes wiring tests for the Envoy egress proxy PR.\n\n` +
          `Repository root is the current working directory. Do not edit files. Run the focused pytest selection below. ` +
          `If dependencies are not installed, first run 'cd server && uv sync --all-groups', then retry pytest.\n\n` +
          `Command:\n` +
          `cd server && uv run pytest tests/k8s/test_egress_helper.py tests/k8s/test_batchsandbox_provider.py tests/k8s/test_agent_sandbox_provider.py tests/k8s/test_kubernetes_service.py -k ${JSON.stringify(serverKeyword)}\n\n` +
          `Classify status as passed, failed, or blocked. Include every command you actually ran, any failure names, and useful artifacts/log paths.`,
        { phase: "server k8s wiring tests", schema: checkResultSchema },
      ),
    );
  }

  const results = await parallel(tasks);
  const egress = results[0];
  const server = includeServer
    ? results[1]
    : {
        status: "blocked",
        summary: "server tests skipped because includeServer=false",
        commands: [],
        failures: [],
        artifacts: [],
      };

  return {
    overallStatus: overallStatus(results),
    checks: { egress, server },
    nextSteps:
      overallStatus(results) === "passed"
        ? ["Build the egress image and run envoy-proxy-docker-smoke for envoy and mitmproxy backends."]
        : ["Fix failed or blocked fast checks before running Docker/Kubernetes/performance validation."],
  };
}
