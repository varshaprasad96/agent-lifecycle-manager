# Agent Lifecycle Manager

A Kubernetes operator that replaces per-pod sidecar injection with a **gateway-based architecture** for AI agent authentication, authorization, identity, and routing. Agents run as plain containers with zero sidecars while a shared per-namespace gateway handles all security and networking concerns.

Built as an alternative to the [kagenti-operator](https://github.com/kagenti/kagenti-operator) sidecar model, inspired by [Models-as-a-Service](https://github.com/opendatahub-io/models-as-a-service) (Gateway API + Kuadrant) and [MCP Gateway](https://github.com/Kuadrant/mcp-gateway) (Envoy ext_proc + protocol-aware routing).

## Why

The kagenti-operator currently injects 2-5 sidecar containers into every agent pod for auth, identity, and observability:

| Container | Purpose | Problem |
|-----------|---------|---------|
| proxy-init | iptables traffic interception | Requires root, NET_ADMIN, custom SCC |
| envoy-proxy | JWT validation, token exchange via ext_proc | ~140MB per pod, duplicated N times |
| spiffe-helper | Fetches SPIFFE SVIDs from SPIRE | Separate sidecar just to write cert files |
| client-registration | Registers agent as OAuth2 client in Keycloak | Distributes admin credentials to every namespace |
| sign-agentcard | Signs agent card with SPIFFE SVID | Signs skeleton card with empty skills (kagenti-operator#292) |

This creates operational burden: privileged containers trigger security scanner flags, Keycloak admin secrets leak across namespace boundaries, five containers consume resources per pod, and upgrading auth requires restarting every agent.

The kagenti waypoint PR ([#259](https://github.com/kagenti/kagenti-operator/pull/259)) proposed zero-sidecar gateway mode but is blocked on Istio ambient mesh dependency. This project provides the same benefit using Gateway API + Envoy Gateway + Kuadrant without requiring Istio.

## Architecture

```
                         Control Plane
                         ──────────────
                         AgentRuntime CR ──> Controller
                                               │
                                       generates│
                                               ▼
                                      HTTPRoute
                                      AuthPolicy
                                      NetworkPolicy
                                      BackendTLSPolicy
                                               │
                         Data Plane             │
                         ──────────             ▼
                    ┌──────────────────────────────────────┐
                    │    Agent Gateway (per namespace)      │
                    │                                      │
  Caller ──────────>│  Envoy Gateway (Gateway API)         │
  (user/agent)      │    │                                 │
                    │    ▼                                 │
                    │  Authorino (Kuadrant AuthPolicy)      │
                    │    ├─ Validate JWT (SPIRE OIDC /      │
                    │    │  Keycloak)                       │
                    │    ├─ Token exchange (RFC 8693)       │
                    │    ├─ Namespace / agent RBAC          │
                    │    └─ Credential stripping            │
                    │    │                                 │
                    │    ▼                                 │
                    │  ext_proc (A2A / MCP Router)         │
                    │    ├─ Parse protocol messages         │
                    │    ├─ Extract method, tool, session   │
                    │    ├─ Set routing headers             │
                    │    └─ Session management              │
                    │    │                                 │
                    │    ▼                                 │
                    │  HTTPRoute ──> agent-service:8000     │
                    └───────────┬──────────────────────────┘
                                │ mTLS (SPIRE)
                    ┌───────────▼──────────────────────────┐
                    │  Agent Pod                           │
                    │  ┌──────────────────────────┐        │
                    │  │ Agent Container           │        │
                    │  │ (LangGraph/CrewAI/AG2)    │        │
                    │  └──────────────────────────┘        │
                    │  SPIRE CSI Volume (identity)         │
                    │  No sidecars                         │
                    └──────────────────────────────────────┘
```

### How It Works

1. **Admin creates a Deployment** (plain agent container, no special labels) and an **AgentRuntime CR** pointing to it.

2. **The controller reconciles** by:
   - Applying `kagenti.io/type=agent` labels and SPIRE CSI volume to the workload
   - Generating an **HTTPRoute** that routes traffic from the gateway to the agent
   - Generating an **AuthPolicy** (Kuadrant) with JWT validation, token exchange, and namespace-based RBAC
   - Generating a **NetworkPolicy** that restricts agent egress to the gateway only (forces all agent-to-agent traffic through the gateway)
   - Generating a **BackendTLSPolicy** for mTLS between gateway and agent via SPIRE

3. **The gateway handles all security** for every agent in the namespace:
   - **Inbound:** Validates caller JWT, checks authorization, exchanges tokens, strips credentials before forwarding
   - **Outbound:** Agent-to-agent calls route through the gateway where token exchange happens per-target
   - **Protocol awareness:** The ext_proc filter parses A2A and MCP messages, extracts method/tool/session info, and sets routing headers for fine-grained authorization

4. **mTLS replaces AgentCard signing.** The gateway connects to agents over mutual TLS using SPIRE X.509 SVIDs. Identity is verified on every connection, not via a stale signed card. The controller fetches the A2A card from the live agent over mTLS on rollout events (event-driven, not polling).

### Key Design Decisions

- **Per-namespace gateway** (not per-cluster): blast radius limited to one namespace, independent scaling per team, natural RBAC boundary
- **Gateway-enforced egress**: NetworkPolicy blocks direct pod-to-pod communication between agents, forcing all traffic through the gateway for centralized auth and audit
- **ext_proc from day one**: A2A/MCP protocol parsing enables tool-level and skill-level authorization, not just coarse agent-level access control
- **Envoy Gateway** (not Istio): lightweight, standalone Gateway API implementation with no service mesh dependency. Code targets the Gateway API spec, so any conformant implementation works
- **Unstructured client for Kuadrant**: AuthPolicy resources generated via unstructured client to avoid hard dependency on Kuadrant API types. Graceful degradation when Kuadrant is absent

## AgentRuntime CRD

The single CRD an admin creates to declare an agent on the platform:

```yaml
apiVersion: agent.kagenti.dev/v1alpha1
kind: AgentRuntime
metadata:
  name: weather-agent
  namespace: team1
spec:
  type: agent                              # agent or tool
  targetRef:
    apiVersion: apps/v1
    kind: Deployment                       # Deployment, StatefulSet, or Sandbox
    name: weather-agent

  identity:
    spiffe:
      trustDomain: example.org             # SPIFFE trust domain for mTLS

  trace:
    endpoint: otel-collector.kagenti-system:4317
    protocol: grpc                         # OTEL exporter protocol

  policy:
    allowedIngressNamespaces:              # who can call this agent
      - team1
      - kagenti-system
    dependencies:                          # what this agent can call (via gateway)
      - name: weather-tool
        allowedTools: [get_temperature]    # tool-level scoping (future)
      - name: data-agent
        namespace: team2
    externalEgress:                        # non-agent external APIs
      - host: api.openweathermap.org
        port: 443
```

The controller generates all gateway resources (HTTPRoute, AuthPolicy, NetworkPolicy, BackendTLSPolicy) from this single CR. Agents without `spec.policy` run unrestricted (backward compatible).

### Status

```yaml
status:
  phase: Active
  configuredPods: 2
  gateway:
    httpRouteName: weather-agent-route
    authPolicyName: weather-agent-auth
    networkPolicyName: weather-agent-netpol
    gatewayEndpoint: https://agent-gateway.team1/agents/weather-agent/
  identity:
    spiffeId: spiffe://example.org/ns/team1/sa/weather-agent
    mtlsEnabled: true
    certificateSource: spire
  card:
    name: weather-agent
    version: "1.0"
    skills: ["weather-forecast", "temperature-lookup"]
    fetchedAt: "2026-05-13T10:00:00Z"
    fetchedOverMTLS: true
  conditions:
    - type: TargetResolved
      status: "True"
    - type: GatewayConfigured
      status: "True"
    - type: PolicyApplied
      status: "True"
    - type: IdentityVerified
      status: "True"
    - type: Ready
      status: "True"
```

## Feature Parity with Kagenti

Every kagenti feature is preserved or improved in the gateway architecture:

### Agent Discovery and Metadata

| Kagenti Feature | How It Works in Gateway Architecture |
|---|---|
| AgentCard CRD (auto-discovery, card caching) | Deprecated per ADR-0002. Replaced by label-based discovery (`kubectl get agentruntimes`) + on-demand A2A card fetch over mTLS. Card always live, never stale. |
| AgentCardSync controller (polls every 30s) | Replaced by event-driven card fetch on workload rollout. Zero polling, zero etcd writes for unchanged cards. |
| `/.well-known/agent-card.json` endpoint | Unchanged. Agents still serve A2A cards at the standard endpoint. Gateway proxies requests to the live agent. |
| Protocol detection (`protocol.kagenti.io/a2a`) | Unchanged. Controller applies the same labels to workloads. |

### Authentication and Identity

| Kagenti Feature | How It Works in Gateway Architecture |
|---|---|
| JWT validation (inbound) | Moved from per-pod envoy ext_proc to shared Authorino via AuthPolicy. Same validation, centralized. |
| Token exchange (RFC 8693, outbound) | Moved from per-pod go-processor to Authorino metadata phase. Same Keycloak endpoint, same protocol. |
| SPIFFE/SPIRE workload identity | Unchanged. SPIRE CSI driver provides X.509 SVIDs to agent pods (replaces spiffe-helper sidecar). |
| Keycloak client registration | Moved from per-pod sidecar to operator-managed or one-time Helm job. Admin credentials removed from agent namespaces. |
| Federated-JWT mode (SVID as client_assertion) | Supported via Authorino's JWT assertion. Same SPIFFE JWT-SVID flow. |
| AgentCard JWS signing (x5c certificate chain) | Replaced by mTLS. Transport-level identity verification on every connection instead of deploy-time card signing. Eliminates skeleton-card problem (kagenti-operator#292). |
| Trust domain binding | Moved to SPIFFE ID validation in TLS handshake (standard X.509 SAN URI matching). |
| Proactive SVID re-signing (pod restarts for cert rotation) | Eliminated. Envoy SDS + SPIRE auto-rotation handles cert refresh with zero downtime, no pod restarts. |

### Network Policy and Authorization

| Kagenti Feature | How It Works in Gateway Architecture |
|---|---|
| NetworkPolicy (verified/unverified agents) | Enhanced. Gateway-enforced egress restricts agents to gateway-only communication. Finer-grained than verified/unverified binary. |
| `allowedIngressNamespaces` | Direct field on AgentRuntime `spec.policy`. Controller generates NetworkPolicy ingress rules. |
| `dependencies` (egress scoping) | Direct field on AgentRuntime `spec.policy`. Controller generates NetworkPolicy egress rules restricted to gateway pods. |
| Istio AuthorizationPolicy (when detected) | Future extension. AuthPolicy CEL expressions can check SPIFFE IDs when Istio is present. |
| OVN-Kubernetes compatibility | Reuses same workarounds from kagenti-operator PR #221 (allow-all egress for verified, API-only for unverified). |

### Protocol Parsing and Observability

| Kagenti Feature | How It Works in Gateway Architecture |
|---|---|
| A2A parser plugin | Moved from per-pod ext_proc to shared gateway ext_proc. Extracts method, sessionId, taskId. Sets `x-a2a-*` routing headers. |
| MCP parser plugin | Moved from per-pod ext_proc to shared gateway ext_proc. Extracts tool name, JSON-RPC method. Sets `x-mcp-*` routing headers. |
| Inference parser plugin | Not in initial scope. Can be added to ext_proc or delegated to a dedicated AI gateway (LiteLLM, Praxis) for streaming inspection. |
| Session API (per-pod `:9094`) | Replaced by OTEL distributed tracing with agent-name labels. ext_proc can expose filtered session API per agent. |
| Stats server (per-pod `:9093`) | Replaced by Kuadrant/Authorino Prometheus metrics per AuthPolicy + gateway-level metrics. |
| Plugin pipeline (composable plugins) | AuthPolicy provides per-agent auth/authz customization. ext_proc handles protocol parsing. Custom plugins via ext_proc header-based branching. |
| Config hot-reload | AuthPolicy changes are picked up by Authorino automatically. Faster than per-pod ConfigMap propagation (~60s kubelet sync). |
| OTEL trace injection | Controller injects OTEL env vars into agent PodTemplateSpec. Same developer experience, no sidecar needed. |

### Workload Management

| Kagenti Feature | How It Works in Gateway Architecture |
|---|---|
| AgentRuntime CRD | Core CRD, extended with `spec.policy` and gateway status fields. |
| 3-layer config merge (cluster > namespace > CR) | Supported via gateway defaults ConfigMap (cluster/namespace level) + per-agent AgentRuntime CR overrides. |
| Feature gates (`kagenti-feature-gates`) | Supported via controller flags and ConfigMap. Policy enforcement gated behind `--enable-policy-enforcement`. |
| Duck typing for workloads (Deployment, StatefulSet, Sandbox) | Unchanged. Controller uses unstructured client to resolve `spec.targetRef` for any workload kind. |
| Finalizer-based cleanup | Unchanged. Controller uses finalizers for graceful deletion of gateway resources. |
| Webhook-based sidecar injection | Eliminated entirely. No mutating webhook needed. Controller directly mutates workload PodTemplateSpec (labels, CSI volume, env vars only). |
| Combined sidecar mode (feature gate) | Superseded. Gateway replaces all sidecar modes. |
| Proxy-sidecar mode (klaviger-inspired) | Superseded. Gateway provides the same benefit (no iptables, no root) at the namespace level. |
| Waypoint mode (Istio ambient) | Superseded. Gateway provides zero-sidecar pods without Istio dependency. |

### Security

| Kagenti Feature | How It Works in Gateway Architecture |
|---|---|
| mTLS (controller-to-agent, agent-to-agent) | Native via SPIRE + Envoy Gateway BackendTLSPolicy. mTLS on every connection, both directions. |
| Credential stripping | Authorino strips original Authorization header before forwarding to agent (defense in depth). |
| No secrets in agent namespaces | Achieved. Keycloak credentials stay in gateway/system namespace. Agents never see admin secrets. |
| iptables enforcement (bypass prevention) | Replaced by NetworkPolicy enforcement. Agent pods can only egress to gateway pods. Not bypassable without CNI-level compromise. |

### Future Extensibility

| Kagenti Feature | How It Works in Gateway Architecture |
|---|---|
| AgentMesh CRD (multi-agent topology) | Natural extension. AgentMesh would generate cross-namespace HTTPRoutes, NetworkPolicies, and AuthPolicies from a single topology declaration. |
| Policy-based authorization (5 layers) | All five layers map to existing components: tool-level (ext_proc headers + AuthPolicy CEL), agent-to-agent (AuthPolicy + dependencies), delegated user auth (RFC 8693 `act` claim chain), per-action runtime (every request hits gateway), tenant isolation (per-namespace gateway + NetworkPolicy). |
| OpenShell integration | Label-based identity handoff. Controller skips auth injection for `openshell.ai/managed-by` pods. Supervisor handles identity, gateway adds OTEL only. |
| Praxis evaluation | Gateway proxy layer is pluggable. If Praxis replaces Envoy, the controller generates Praxis-native resources instead of HTTPRoute/AuthPolicy. The AgentRuntime CRD doesn't change. |
| Rate limiting (per agent, per tool) | Kuadrant RateLimitPolicy (Limitador) attaches to per-agent HTTPRoutes. Controller generates rate limit rules from AgentRuntime spec. |

## Known Limitations

- **Streaming body inspection is limited.** Envoy ext_proc operates in HEADERS_ONLY or BUFFERED mode -- it cannot inspect streaming response bodies chunk-by-chunk. The inference-parser plugin (which tracks token counts from streaming LLM responses) and real-time guardrails on streaming content cannot run inline at the gateway. Mitigation: delegate streaming content inspection to a dedicated AI gateway layer (LiteLLM, Praxis) or async mirror.

- **Gateway is a shared failure domain.** All agents in a namespace share one gateway. A gateway outage or misconfiguration affects every agent, not just one. Sidecars isolate failures to individual pods. Mitigation: run gateway with multiple replicas, PodDisruptionBudgets, and anti-affinity rules.

- **Added network hop for every request.** Sidecar proxies run on localhost with zero network latency. The gateway adds a network hop (typically 1-5ms in-cluster). For high-frequency, low-latency agent-to-agent chains, this compounds. Mitigation: negligible for most agent workloads where LLM calls dominate latency.

- **Per-agent plugin customization is constrained.** The sidecar model allows each agent to run a different plugin pipeline. The gateway shares one ext_proc across all agents. Agents needing custom body processing (e.g., a compliance plugin specific to one agent) would need ext_proc header-based branching or an escape-hatch sidecar.

- **NetworkPolicy enforcement varies by CNI.** OVN-Kubernetes silently drops port-only egress rules. Some CNIs don't enforce NetworkPolicy at all. The gateway-enforced egress model depends on a conformant CNI. Mitigation: reuse OVN-K workarounds from kagenti-operator PR #221; test against Calico, Cilium, OVN-K.

- **Localhost trust boundary is lost.** Sidecar-to-agent communication is over localhost (inherently secure). Gateway-to-agent crosses the pod network and requires mTLS to secure. This is additional complexity that sidecars avoid by co-location.

- **Debugging is less direct.** With sidecars, `kubectl logs <pod> -c envoy-proxy` shows one agent's proxy logs. With a shared gateway, all agents' traffic is in the same logs. Requires distributed tracing (OTEL) with agent-name labels for filtering.

- **SPIFFE file-based SVID consumption changes.** Agents that read `/opt/jwt_svid.token` or `/opt/svid.pem` directly (the spiffe-helper file contract) need to switch to SPIRE CSI driver paths (`/spiffe-certs/`) or the Workload API socket.

## Getting Started

### Prerequisites

- Go 1.24+
- kubectl v1.28+
- Access to a Kubernetes v1.28+ cluster
- [Envoy Gateway](https://gateway.envoyproxy.io/) installed
- [Kuadrant](https://kuadrant.io/) installed (Authorino + Limitador)
- [SPIRE](https://spiffe.io/docs/latest/spire-about/) deployed (for mTLS)

### Install CRDs

```sh
make install
```

### Deploy the Controller

```sh
make deploy IMG=<registry>/agent-lifecycle-manager:tag
```

### Create an Agent

```sh
# Deploy a plain agent workload
kubectl apply -f config/samples/weather-agent-deployment.yaml

# Create an AgentRuntime CR
kubectl apply -f config/samples/agent_v1alpha1_agentruntime.yaml

# Verify
kubectl get agentruntimes
```

### Uninstall

```sh
kubectl delete -k config/samples/
make uninstall
make undeploy
```

## Project Structure

```
cmd/
  manager/main.go                  # Operator entrypoint
  agent-router/main.go             # ext_proc binary (A2A/MCP router)
api/v1alpha1/
  agentruntime_types.go            # AgentRuntime CRD types
internal/
  controller/
    agentruntime_controller.go     # Main reconciler
    gateway_resources.go           # HTTPRoute + AuthPolicy generation
    network_policy.go              # NetworkPolicy generation
    workload_mutator.go            # SPIRE CSI + OTEL + label injection
    backend_tls.go                 # BackendTLSPolicy for mTLS
  router/
    server.go                      # ext_proc gRPC server
    a2a_parser.go                  # A2A protocol parser
    mcp_parser.go                  # MCP protocol parser
    session.go                     # Session management
config/
  crd/                             # Generated CRD manifests
  samples/                         # Example AgentRuntime CRs
charts/
  agent-lifecycle-manager/         # Helm chart
```

## License

Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
