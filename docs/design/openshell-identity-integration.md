# OpenShell Identity Integration — Design Proposal

**Status:** Proposal  
**Authors:** Varsha Prasad Narsing  
**Date:** 2026-05-15  
**Related:** [OpenShell](https://github.com/NVIDIA/OpenShell), [kagenti ADR-0002](https://docs.google.com/document/d/1kb9TleypXYtkgRDi4r4ik7uDL0c0gEFlVlNpzbYQxS4), [Platform Architecture Doc](https://docs.google.com/document/d/1lI6c6M1LY184pIOJPd61lHjliMq5Ntbl4ppB1mucWGE)

---

## Context

The agent-lifecycle-manager operator provides a gateway-based architecture for AI agent lifecycle management on Kubernetes. It replaces per-pod sidecar injection with a shared per-namespace gateway that handles auth, token exchange, and routing. Agents run as plain containers with zero sidecars.

[OpenShell](https://github.com/NVIDIA/OpenShell) is NVIDIA's sandboxed execution runtime for AI agents. It provides six isolation layers (network namespaces, OPA policy, L7 inspection, credential placeholder rewriting, Landlock filesystem restrictions, seccomp syscall filtering) but has **no authentication or identity implementation** as of today. The identity work is RFC-approved upstream but has zero code.

These two projects are complementary layers:

- **OpenShell** solves: "how do I stop an agent from escaping its sandbox?"
- **Agent gateway** solves: "how do I control what agents can talk to each other, and with what permissions?"

The missing piece in both is **identity**. This document proposes how the AgentRuntime CRD can serve as a single control plane for both plain and sandboxed agents, with a unified identity model backed by SPIRE.

---

## Two Deployment Models

### Model 1: Plain Agent (No Sandbox)

The agent runs as a standard Kubernetes Deployment. The gateway handles all security concerns.

```
AgentRuntime CR → Controller →
  ├── Labels + SPIRE CSI volume (agent gets its own SVID)
  ├── HTTPRoute (gateway routing)
  ├── AuthPolicy (JWT validation, token exchange)
  ├── NetworkPolicy (egress locked to gateway)
  └── BackendTLSPolicy (mTLS between gateway and agent)

Gateway handles: identity, auth, authorization, network isolation, routing, observability
Agent handles: application logic only
```

This is what the operator implements today (Phases 1-7).

### Model 2: OpenShell-Sandboxed Agent

The agent runs inside an OpenShell sandbox. The supervisor (PID 1) handles security. The gateway handles routing and observability only.

```
AgentRuntime CR → Controller detects openshell.ai/managed-by label →
  ├── Labels (discovery)
  ├── OTEL env vars (observability)
  ├── HTTPRoute (gateway routing)
  │
  ├── SKIP: SPIRE CSI volume (supervisor gets the SVID, not the agent)
  ├── SKIP: AuthPolicy (supervisor handles auth)
  ├── SKIP: NetworkPolicy (sandbox has its own network namespace isolation)
  └── SKIP: BackendTLSPolicy (supervisor handles mTLS)

Supervisor handles: identity, auth, network isolation, filesystem isolation, syscall filtering
Gateway handles: routing, observability
```

Both models share the same gateway for routing and observability but diverge on who owns identity and security enforcement.

---

## The Identity Gap

### The Problem: Pod-Level vs Process-Level Identity

SPIRE attests identity at the **pod level** using kernel-level attributes (cgroups, namespace IDs, ServiceAccount tokens). For plain agents (Model 1), this is sufficient — each pod runs a single trusted binary.

For OpenShell sandboxes (Model 2), the trust boundary is **inside the pod**:

```
OpenShell Sandbox Pod (one SPIFFE SVID)
├── Supervisor (PID 1, Rust binary, trusted platform code)
│     Can access: SPIRE socket, Landlock config, OPA policies, signing keys
│
└── Agent process (child of PID 1, untrusted, possibly adversarial)
      Cannot access: SPIRE socket (Landlock blocks it)
      Cannot access: network directly (separate network namespace)
      Cannot access: filesystem outside its sandbox (Landlock)
```

If the agent process could access the SPIRE socket, it would get the same SVID as the supervisor. A compromised agent could then impersonate the supervisor in calls to MCP Gateway, other agents, or external services. The six isolation layers OpenShell provides all answer "can this process reach this host?" — none of them answer "who is this process?"

### The Solution: Supervisor as Intermediate CA

The proposed model is a two-tier SPIFFE identity hierarchy where the supervisor acts as an intermediate certificate authority:

```
SPIRE (cluster trust root)
│
└── spiffe://example.org/ns/team1/sa/coding-agent        (pod-level SVID)
     │     Held exclusively by the supervisor.
     │     Landlock blocks agent process from SPIRE socket.
     │
     ├── spiffe://...coding-agent/supervisor               (supervisor's working identity)
     │
     └── spiffe://...coding-agent/process/agent            (derived sub-identity)
           Signed by supervisor's key.
           Chains to SPIRE root via supervisor cert.
           Encodes constraints:
             - allowed-tools: [github-tool, search]
             - owner: alex@redhat.com
             - ttl: 1 hour
             - no signing authority (cannot issue further sub-identities)
```

### Why the Agent Cannot Escalate

The derived identity is provably weaker than the parent through four mechanisms:

1. **Shorter TTL.** The agent sub-identity expires in 1 hour. The supervisor's lasts the sandbox lifetime. A compromised credential becomes useless quickly.

2. **Narrower scope.** The agent sub-identity lists specific allowed tools. There is no way to widen the scope from inside the sandbox.

3. **No signing authority.** The agent's key pair cannot issue further certificates. Only the supervisor can delegate.

4. **Landlock enforcement.** The kernel prevents the agent process from accessing the SPIRE socket or the supervisor's key material, even with arbitrary code execution inside the sandbox.

### Two Approaches to SPIRE Integration

**Approach A (recommended): Supervisor as intermediate CA.** The supervisor uses its pod-level SVID as a signing key for agent certificates. External services validate the full chain: agent cert → supervisor cert → SPIRE root. SPIRE does not need to know about sub-identities. The supervisor takes responsibility for what it signs. Given that the supervisor is already the security boundary (proxy, Landlock, network namespace), this additional trust is acceptable and requires no upstream SPIRE changes.

**Approach B: SPIRE Delegated Identity API.** SPIRE's existing delegation mechanism authorizes supervisors to issue subordinate SVIDs. More aligned with the SPIFFE specification, but adds operational complexity because SPIRE needs to know about every supervisor's delegation authority.

---

## AgentRuntime CRD Changes

### New Field: `spec.identity.mode`

```yaml
apiVersion: agent.kagenti.dev/v1alpha1
kind: AgentRuntime
metadata:
  name: coding-agent
  namespace: team1
spec:
  type: agent
  targetRef:
    kind: Sandbox
    name: coding-agent
    apiVersion: agents.x-k8s.io/v1alpha1

  identity:
    spiffe:
      trustDomain: example.org
    # NEW FIELD
    mode: supervisor-delegated     # "direct" (default) or "supervisor-delegated"

  trace:
    endpoint: otel-collector:4317

  policy:
    dependencies:
      - name: github-tool
        allowedTools: [list_files, read_file]
```

| `identity.mode` | Meaning | Controller behavior |
|---|---|---|
| `direct` (default) | Agent gets its own SVID via SPIRE CSI | Inject CSI volume, create AuthPolicy, NetworkPolicy, BackendTLSPolicy |
| `supervisor-delegated` | Supervisor holds SVID, issues sub-identity to agent | Skip CSI/AuthPolicy/NetworkPolicy/BackendTLSPolicy. Create HTTPRoute + OTEL only |

The controller auto-detects `supervisor-delegated` when the workload has the `openshell.ai/managed-by` label, even if the field is not explicitly set.

### Updated Status

```yaml
status:
  phase: Active
  identity:
    spiffeId: spiffe://example.org/ns/team1/sa/coding-agent
    mode: supervisor-delegated
    mtlsEnabled: true
    certificateSource: supervisor     # "spire" for direct, "supervisor" for delegated
  gateway:
    httpRouteName: coding-agent-route
    gatewayEndpoint: https://agent-gateway.team1/agents/coding-agent/
    # No authPolicyName, networkPolicyName — supervisor handles these
  conditions:
    - type: TargetResolved
      status: "True"
    - type: GatewayConfigured
      status: "True"
    - type: IdentityDelegated
      status: "True"
      reason: SupervisorManaged
      message: "Identity delegated to OpenShell supervisor"
    - type: Ready
      status: "True"
```

---

## Controller Reconciliation Path

```
Reconcile(AgentRuntime):
│
├── resolveTargetRef()                    // same for both models
├── applyLabels()                         // same for both models
├── injectOTEL()                          // same for both models
├── reconcileHTTPRoute()                  // same for both models
│
├── if identity.mode == "direct":
│     ├── injectSpireCSI()
│     ├── reconcileAuthPolicy()
│     ├── reconcileNetworkPolicy()
│     ├── reconcileBackendTLSPolicy()
│     └── fetchAgentCard()                // direct mTLS fetch
│
└── if identity.mode == "supervisor-delegated":
      ├── skip CSI, AuthPolicy, NetworkPolicy, BackendTLSPolicy
      ├── setCondition(IdentityDelegated=True)
      └── fetchAgentCard()                // via supervisor's mTLS
```

---

## Gateway Validation — Both Models, Same Trust Root

The gateway does not need to distinguish between the two models at request time. Both produce a certificate chain that validates against the same SPIRE trust root:

```
Plain agent:       agent SVID ─────────────────────→ SPIRE CA     (one hop)
Sandboxed agent:   agent cert → supervisor SVID ───→ SPIRE CA     (two hops)
```

Standard X.509 chain validation handles both. The BackendTLSPolicy for the gateway's own SPIRE identity validates inbound connections from both models identically.

The difference shows up in the certificate's SPIFFE ID path and embedded constraints:

```
Plain agent SVID:
  spiffe://example.org/ns/team1/sa/weather-agent
  No constraints embedded (pod-level identity, full authority)

Sandboxed agent cert:
  spiffe://example.org/ns/team1/sa/coding-agent/process/agent
  Constraints (X.509 extensions or SAN URI parameters):
    - allowed-tools: [github-tool, search]
    - owner: alex@redhat.com
    - ttl: 1h
    - max-delegation-depth: 0 (no sub-sub-identities)
```

AuthPolicy CEL expressions can optionally inspect these constraints for fine-grained authorization:

```yaml
authorization:
  tool-scope-check:
    patternMatching:
      patterns:
        - predicate: >
            !has(auth.identity.constraints.allowed_tools) ||
            request.headers['x-mcp-toolname'] in auth.identity.constraints.allowed_tools
```

This is additive — agents without constraints (plain agents) pass the check by default.

---

## Portability Across Compute Drivers

One of the strongest properties of OpenShell's identity model is that the sub-identity pattern does not require SPIRE on non-Kubernetes footprints. What changes is how the supervisor gets its parent identity:

| Footprint | Parent Identity Source | Agent Sub-Identity |
|---|---|---|
| Kubernetes | SPIFFE SVID from SPIRE | Derived cert signed by supervisor |
| Podman (laptop) | Self-signed cert by gateway | Derived cert signed by supervisor |
| Podman (remote) | Gateway-issued cert (OIDC-bootstrapped) | Derived cert signed by supervisor |

The supervisor's delegation logic stays identical across all footprints. The developer experience is the same everywhere.

On Kubernetes with our operator, the AgentRuntime CR provides the additional layer: routing through the gateway, OTEL injection, and platform-level discovery. Off-Kubernetes, OpenShell handles everything independently.

---

## What Needs Building

### In This Project (agent-lifecycle-manager)

| Item | Complexity | Description |
|---|---|---|
| `identity.mode` field on AgentRuntime CRD | Low | Add field, update deepcopy, regenerate CRD |
| Controller conditional branch | Low | Skip auth/network/TLS injection when `supervisor-delegated` |
| Auto-detect `openshell.ai/managed-by` label | Low | Set `identity.mode` automatically based on workload label |
| `IdentityDelegated` condition | Low | New condition type for status |
| Constraint-aware AuthPolicy CEL | Medium | Optional CEL expressions that read allowed-tools/owner from cert |

### In OpenShell (upstream, NVIDIA)

| Item | Complexity | Description |
|---|---|---|
| Supervisor intermediate CA | High | Rust code to generate keypair, sign agent certs with pod SVID |
| Landlock policy for SPIRE socket | Low | Block agent process from `/spiffe-workload-api/` |
| Sub-identity file contract | Low | Write agent cert/key to a path the agent can read |
| SPIRE CSI driver integration | Medium | Mount SPIRE Workload API socket into supervisor's namespace only |

### Shared / Integration

| Item | Complexity | Description |
|---|---|---|
| SPIFFE ID path convention | Design | Agree on `spiffe://.../process/agent` path format |
| Constraint encoding format | Design | X.509 extensions vs SAN URI parameters for allowed-tools/owner/ttl |
| Integration test: gateway validates supervisor-issued certs | Medium | E2E test with SPIRE + OpenShell + gateway |

---

## What Exists Today vs What's Proposed

| Component | Status | Owner |
|---|---|---|
| SPIRE cluster deployment | Exists | kagenti platform |
| SPIRE CSI driver | Exists | SPIFFE upstream |
| Agent gateway with HTTPRoute/AuthPolicy | Exists | This project (Phases 1-7) |
| OpenShell sandbox isolation (6 layers) | Exists | NVIDIA/OpenShell |
| `openshell.ai/managed-by` label convention | Proposed | OpenShell community |
| Supervisor intermediate CA | RFC approved, zero code | OpenShell upstream |
| `identity.mode` on AgentRuntime | Proposed in this doc | This project |
| Constraint-aware AuthPolicy CEL | Proposed in this doc | This project |
| Sub-identity file contract | Proposed | OpenShell + this project |
| SPIFFE ID path convention | Needs design | SPIFFE community |

---

## Why This Matters

No open-source project currently provides a unified agent identity platform that works across both plain and sandboxed deployment models. Microsoft's Entra Agent ID is Azure-only. SPIFFE/SPIRE provides workload identity but not process-level sub-identities. OpenShell provides isolation but not identity.

If we ship the identity layer for both paths — SPIRE CSI for plain agents, supervisor sub-identity for sandboxed agents, shared SPIRE trust root, single AgentRuntime CRD as the control plane — that is the first open-source agent identity platform.

The AgentRuntime CRD is the right abstraction because it already represents "everything the platform needs to know about this agent" regardless of workload type. Adding `identity.mode` is a natural extension that keeps the one-CR-per-agent principle from kagenti's ADR-0002.

---

## Open Questions

1. **SPIFFE ID path convention for sub-identities.** Should it be `spiffe://.../process/agent` or `spiffe://.../agent/<name>`? Does the SPIFFE spec allow arbitrary path extensions, or do we need a community RFC?

2. **Constraint encoding.** X.509 certificate extensions (OID-based, standard but opaque to most tooling) vs SAN URI parameters (e.g., `spiffe://...?allowed-tools=github-tool&ttl=1h` — non-standard but human-readable)?

3. **Supervisor CA trust.** Should the gateway trust any supervisor-signed cert that chains to SPIRE, or should there be an explicit registration step where the supervisor's signing authority is recorded (e.g., in the AgentRuntime CR or a ConfigMap)?

4. **Off-Kubernetes parity.** On Podman/laptop, there is no AgentRuntime CR or gateway. Should the supervisor's identity behavior be identical regardless? Or is it acceptable for the Kubernetes path to have richer policy via the gateway?

5. **OpenShell community alignment.** The `openshell.ai/managed-by` label and the sub-identity file contract need to be proposed upstream. How do we ensure NVIDIA's community is onboard before building the integration?
