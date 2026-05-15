#!/usr/bin/env bash
set -euo pipefail

CLUSTER_NAME="${CLUSTER_NAME:-agent-lifecycle}"
NAMESPACE="${NAMESPACE:-agent-system}"

echo "==> Creating Kind cluster: ${CLUSTER_NAME}"
kind create cluster --name "${CLUSTER_NAME}" --wait 60s 2>/dev/null || echo "Cluster ${CLUSTER_NAME} already exists"

echo "==> Installing Gateway API CRDs"
kubectl apply -f https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.2.1/standard-install.yaml

echo "==> Installing Envoy Gateway"
helm install eg oci://docker.io/envoyproxy/gateway-helm \
  --version v1.3.0 \
  -n envoy-gateway-system --create-namespace \
  --wait 2>/dev/null || echo "Envoy Gateway already installed"

echo "==> Waiting for Envoy Gateway to be ready"
kubectl wait --timeout=120s -n envoy-gateway-system \
  deployment/envoy-gateway \
  --for=condition=Available

echo "==> Installing CRDs"
make install

echo "==> Creating namespace: ${NAMESPACE}"
kubectl create namespace "${NAMESPACE}" 2>/dev/null || true

echo "==> Creating gateway config"
kubectl apply -f - <<EOF
apiVersion: v1
kind: ConfigMap
metadata:
  name: agent-gateway-config
  namespace: ${NAMESPACE}
data:
  issuer-url: ""
  token-exchange-url: ""
  trust-domain: "example.org"
EOF

echo "==> Creating Gateway"
kubectl apply -f - <<EOF
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  name: agent-gateway
  namespace: ${NAMESPACE}
spec:
  gatewayClassName: eg
  listeners:
    - name: agent-http
      port: 8080
      protocol: HTTP
      allowedRoutes:
        namespaces:
          from: Same
EOF

echo "==> Deploying controller (local)"
make run &
MANAGER_PID=$!
echo "Manager PID: ${MANAGER_PID}"

echo ""
echo "==> Setup complete!"
echo "    Cluster: ${CLUSTER_NAME}"
echo "    Namespace: ${NAMESPACE}"
echo "    Manager PID: ${MANAGER_PID}"
echo ""
echo "Next steps:"
echo "  kubectl apply -f config/samples/weather-agent-deployment.yaml"
echo "  kubectl apply -f config/samples/agent_v1alpha1_agentruntime.yaml"
echo "  kubectl get agentruntimes -n ${NAMESPACE}"
echo ""
echo "To stop: kill ${MANAGER_PID}"
