/*
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
*/

package controller

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	agentv1alpha1 "github.com/varshaprasad96/agent-lifecycle-manager/api/v1alpha1"
)

const (
	conditionTypeAuthConfigured = "AuthConfigured"

	gatewayConfigMapName = "agent-gateway-config"

	configKeyIssuerURL        = "issuer-url"
	configKeyTokenExchangeURL = "token-exchange-url"
	configKeyTrustDomain      = "trust-domain"
)

var authPolicyGVR = schema.GroupVersionResource{
	Group:    "kuadrant.io",
	Version:  "v1",
	Resource: "authpolicies",
}

var authPolicyGVK = schema.GroupVersionKind{
	Group:   "kuadrant.io",
	Version: "v1",
	Kind:    "AuthPolicy",
}

// GatewayConfig holds auth-related configuration read from the gateway ConfigMap.
type GatewayConfig struct {
	IssuerURL        string
	TokenExchangeURL string
	TrustDomain      string
}

func authPolicyName(ar *agentv1alpha1.AgentRuntime) string {
	return ar.Name + "-auth"
}

func (r *AgentRuntimeReconciler) reconcileAuthPolicy(ctx context.Context, ar *agentv1alpha1.AgentRuntime) error {
	log := logf.FromContext(ctx)

	if !r.isAuthPolicyCRDAvailable(ctx) {
		log.Info("AuthPolicy CRD not found, skipping auth policy generation")
		r.setCondition(ar, conditionTypeAuthConfigured, metav1.ConditionFalse, "KuadrantNotFound",
			"Kuadrant AuthPolicy CRD not available on this cluster")
		return nil
	}

	gwConfig, err := r.readGatewayConfig(ctx, ar.Namespace)
	if err != nil {
		log.Info("gateway config not found, skipping auth policy", "error", err)
		r.setCondition(ar, conditionTypeAuthConfigured, metav1.ConditionFalse, "GatewayConfigMissing",
			fmt.Sprintf("ConfigMap %s not found: %v", gatewayConfigMapName, err))
		return nil
	}

	desired := buildAuthPolicy(ar, gwConfig)
	if err := ctrl.SetControllerReference(ar, desired, r.Scheme); err != nil {
		return fmt.Errorf("setting owner reference on AuthPolicy: %w", err)
	}

	existing := &unstructured.Unstructured{}
	existing.SetGroupVersionKind(authPolicyGVK)
	err = r.Get(ctx, types.NamespacedName{Name: desired.GetName(), Namespace: desired.GetNamespace()}, existing)
	if errors.IsNotFound(err) {
		log.Info("creating AuthPolicy", "name", desired.GetName())
		return r.Create(ctx, desired)
	}
	if err != nil {
		return fmt.Errorf("getting AuthPolicy: %w", err)
	}

	spec, _, _ := unstructured.NestedMap(desired.Object, "spec")
	_ = unstructured.SetNestedMap(existing.Object, spec, "spec")
	log.Info("updating AuthPolicy", "name", existing.GetName())
	return r.Update(ctx, existing)
}

func (r *AgentRuntimeReconciler) isAuthPolicyCRDAvailable(ctx context.Context) bool {
	_, err := r.Client.(client.Client).RESTMapper().RESTMapping(
		schema.GroupKind{Group: "kuadrant.io", Kind: "AuthPolicy"},
		"v1",
	)
	return err == nil
}

func (r *AgentRuntimeReconciler) readGatewayConfig(ctx context.Context, namespace string) (*GatewayConfig, error) {
	cm := &corev1.ConfigMap{}
	if err := r.Get(ctx, types.NamespacedName{Name: gatewayConfigMapName, Namespace: namespace}, cm); err != nil {
		return nil, err
	}

	return &GatewayConfig{
		IssuerURL:        cm.Data[configKeyIssuerURL],
		TokenExchangeURL: cm.Data[configKeyTokenExchangeURL],
		TrustDomain:      cm.Data[configKeyTrustDomain],
	}, nil
}

func buildAuthPolicy(ar *agentv1alpha1.AgentRuntime, gwConfig *GatewayConfig) *unstructured.Unstructured {
	ap := &unstructured.Unstructured{}
	ap.SetGroupVersionKind(authPolicyGVK)
	ap.SetName(authPolicyName(ar))
	ap.SetNamespace(ar.Namespace)
	ap.SetLabels(map[string]string{
		labelManagedBy: managerName,
		labelType:      ar.Spec.Type,
	})

	spec := map[string]interface{}{
		"targetRef": map[string]interface{}{
			"group": "gateway.networking.k8s.io",
			"kind":  "HTTPRoute",
			"name":  httpRouteName(ar),
		},
		"rules": buildAuthPolicyRules(ar, gwConfig),
	}

	_ = unstructured.SetNestedMap(ap.Object, spec, "spec")
	return ap
}

func buildAuthPolicyRules(ar *agentv1alpha1.AgentRuntime, gwConfig *GatewayConfig) map[string]interface{} {
	rules := map[string]interface{}{}

	if gwConfig.IssuerURL != "" {
		rules["authentication"] = map[string]interface{}{
			"jwt-authn": map[string]interface{}{
				"jwt": map[string]interface{}{
					"issuerUrl": gwConfig.IssuerURL,
				},
			},
		}
	}

	if ar.Spec.Policy != nil && len(ar.Spec.Policy.AllowedIngressNamespaces) > 0 {
		namespacesStr := make([]interface{}, len(ar.Spec.Policy.AllowedIngressNamespaces))
		for i, ns := range ar.Spec.Policy.AllowedIngressNamespaces {
			namespacesStr[i] = ns
		}

		predicate := fmt.Sprintf(
			"auth.identity.namespace in ['%s']",
			strings.Join(ar.Spec.Policy.AllowedIngressNamespaces, "', '"),
		)
		rules["authorization"] = map[string]interface{}{
			"namespace-check": map[string]interface{}{
				"patternMatching": map[string]interface{}{
					"patterns": []interface{}{
						map[string]interface{}{
							"predicate": predicate,
						},
					},
				},
			},
		}
	}

	if gwConfig.TokenExchangeURL != "" {
		rules["metadata"] = map[string]interface{}{
			"token-exchange": map[string]interface{}{
				"http": map[string]interface{}{
					"url":    gwConfig.TokenExchangeURL,
					"method": "POST",
					"bodyParameters": map[string]interface{}{
						"grant_type": map[string]interface{}{
							"value": "urn:ietf:params:oauth:grant-type:token-exchange",
						},
						"subject_token": map[string]interface{}{
							"expression": "request.headers['authorization'].split('Bearer ')[1]",
						},
						"subject_token_type": map[string]interface{}{
							"value": "urn:ietf:params:oauth:token-type:access_token",
						},
						"audience": map[string]interface{}{
							"value": ar.Name,
						},
					},
				},
			},
		}

		rules["response"] = map[string]interface{}{
			"success": map[string]interface{}{
				"headers": map[string]interface{}{
					"authorization": map[string]interface{}{
						"plain": map[string]interface{}{
							"expression": `"Bearer " + auth.metadata.token-exchange.access_token`,
						},
					},
				},
			},
		}
	}

	return rules
}
