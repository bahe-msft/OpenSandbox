export const meta = {
  name: "envoy-proxy-docker-smoke",
  description: "Run a Docker black-box smoke test for one egress transparent proxy backend (envoy or mitmproxy), using exe-dev sandbox isolation by default.",
};

const smokeResultSchema = {
  type: "object",
  properties: {
    status: { type: "string", enum: ["passed", "failed", "blocked"] },
    backend: { type: "string" },
    summary: { type: "string" },
    commands: { type: "array", items: { type: "string" } },
    checks: {
      type: "array",
      items: {
        type: "object",
        properties: {
          name: { type: "string" },
          status: { type: "string", enum: ["passed", "failed", "blocked", "skipped"] },
          details: { type: "string" },
        },
        required: ["name", "status", "details"],
      },
    },
    failures: { type: "array", items: { type: "string" } },
    artifacts: { type: "array", items: { type: "string" } },
    cleanupStatus: { type: "string" },
    sandboxProfile: { type: "string" },
  },
  required: ["status", "backend", "summary", "commands", "checks", "failures", "artifacts", "cleanupStatus", "sandboxProfile"],
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
  const backend = String(cfg.backend || "envoy").toLowerCase();
  if (!["envoy", "mitmproxy"].includes(backend)) {
    throw new Error(`backend must be 'envoy' or 'mitmproxy', got ${backend}`);
  }

  const image = String(cfg.image || "opensandbox/egress:envoy-local");
  const containerName = String(cfg.containerName || `egress-${backend}-smoke`);
  const policyPort = Number(cfg.policyPort || 18080);
  const token = String(cfg.token || "test-token");
  const allowHost = String(cfg.allowHost || "example.com");
  const denyHost = String(cfg.denyHost || "github.com");
  const credentialHost = String(cfg.credentialHost || "httpbin.org");
  const runCredentialVault = boolArg(cfg.runCredentialVault, true);
  const buildImage = boolArg(cfg.buildImage, true);
  const cleanup = boolArg(cfg.cleanup, true);
  const rawSandboxProfile = cfg.sandboxProfile === undefined ? "exe-dev/default" : String(cfg.sandboxProfile);
  const sandboxProfile = ["", "none", "host", "false"].includes(rawSandboxProfile.toLowerCase())
    ? null
    : rawSandboxProfile;
  const agentOptions = { phase: `${backend} docker smoke`, schema: smokeResultSchema };
  if (sandboxProfile) {
    agentOptions.isolation = { type: "sandbox", profile: sandboxProfile };
  }

  phase(`${backend} docker smoke`);

  return agent(
    `You are running a black-box Docker smoke test for OpenSandbox egress transparent proxy backend '${backend}'.\n\n` +
      `Repository root is the current working directory. Do not edit repository files. Use Docker commands only.\n\n` +
      `Configuration:\n` +
      `- backend: ${backend}\n` +
      `- sandboxProfile: ${sandboxProfile || "none (host execution)"}\n` +
      `- image: ${image}\n` +
      `- containerName: ${containerName}\n` +
      `- host policy API port: ${policyPort}\n` +
      `- egress token: ${token}\n` +
      `- allowHost: ${allowHost}\n` +
      `- denyHost: ${denyHost}\n` +
      `- runCredentialVault: ${runCredentialVault}\n` +
      `- credentialHost: ${credentialHost}\n` +
      `- buildImage: ${buildImage}\n` +
      `- cleanup: ${cleanup}\n\n` +
      `Execution environment:\n` +
      (sandboxProfile
        ? `- This agent call is isolated with smol-workflows sandbox profile ${sandboxProfile}. You should be running inside an exe.dev VM workspace synced from the repository.\n`
        : `- This agent call is not using smol-workflows sandbox isolation; commands run in the host workspace.\n`) +
      `- Docker validation commands run in that execution environment. If Docker is unavailable there, report status=blocked instead of falling back to a different environment.\n\n` +
      `Required procedure:\n` +
      `1. Check that docker is available in the current execution environment. If not, status=blocked.\n` +
      `2. If buildImage=true, run: docker build -t ${image} -f components/egress/Dockerfile .\n` +
      `3. Remove any stale container named ${containerName}.\n` +
      `4. Start the sidecar container with NET_ADMIN, IPv6 disabled, OPENSANDBOX_EGRESS_MODE=dns+nft, ` +
      `OPENSANDBOX_EGRESS_MITMPROXY_TRANSPARENT=true, OPENSANDBOX_EGRESS_TOKEN=${token}, ` +
      `and OPENSANDBOX_EGRESS_HTTP_PROXY_BACKEND=${backend}. Publish container port 18080 to host port ${policyPort}.\n` +
      (backend === "envoy"
        ? `   Also pass OPENSANDBOX_EGRESS_ENVOY_MITM_HOSTS=${allowHost}${runCredentialVault ? `,${credentialHost}` : ""}.\n`
        : ``) +
      `5. Poll http://127.0.0.1:${policyPort}/healthz with header OPENSANDBOX-EGRESS-AUTH: ${token} until ready, up to 60s.\n` +
      `6. POST a default-deny policy allowing ${allowHost}${runCredentialVault ? ` and ${credentialHost}` : ""}.\n` +
      `7. Verify allowed HTTPS succeeds: docker exec ${containerName} curl -sfI --max-time 10 https://${allowHost}\n` +
      `8. Verify denied HTTPS fails closed: docker exec ${containerName} sh -c '! curl -sfI --max-time 5 https://${denyHost}'\n` +
      `9. Verify CA export exists: docker exec ${containerName} test -s /opt/opensandbox/mitmproxy-ca-cert.pem\n` +
      `10. Inspect iptables/nft/process state enough to debug failures.\n` +
      (runCredentialVault
        ? `11. Create a Credential Vault binding for https://${credentialHost}/headers that injects header X-OpenSandbox-Test with value not-a-real-secret. Then run docker exec ${containerName} curl -s --max-time 15 https://${credentialHost}/headers and verify the echoed response contains both the header name and value. Also inspect logs and report whether the secret value appears in logs.\n`
        : `11. Skip Credential Vault because runCredentialVault=false.\n`) +
      `12. If cleanup=true, remove ${containerName} at the end even on failure.\n\n` +
      `Classify each check individually. If any command fails, collect docker logs --tail=300 ${containerName} before cleanup when possible. ` +
      `Return a structured result with every command you actually ran, artifacts/log paths, and sandboxProfile=${sandboxProfile || "none"}.`,
    agentOptions,
  );
}
