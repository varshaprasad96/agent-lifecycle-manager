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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	agentv1alpha1 "github.com/varshaprasad96/agent-lifecycle-manager/api/v1alpha1"
)

var _ = Describe("AuthPolicy", func() {
	const (
		namespace  = "default"
		deployName = "auth-test-agent"
		arName     = "auth-test-agentruntime"
	)

	ctx := context.Background()
	reconciler := func() *AgentRuntimeReconciler {
		return &AgentRuntimeReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
	}

	Context("When Kuadrant CRD is not installed", func() {
		BeforeEach(func() {
			deploy := newTestDeployment(deployName, namespace)
			Expect(k8sClient.Create(ctx, deploy)).To(Succeed())
		})

		AfterEach(func() {
			cleanupAgentRuntime(ctx, arName, namespace, reconciler())
			cleanupDeployment(ctx, deployName, namespace)
		})

		It("should set AuthConfigured=False with KuadrantNotFound reason", func() {
			ar := newTestAgentRuntime(arName, namespace, deployName)
			Expect(k8sClient.Create(ctx, ar)).To(Succeed())

			_, err := reconciler().Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: arName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			updated := &agentv1alpha1.AgentRuntime{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: arName, Namespace: namespace}, updated)).To(Succeed())

			var authCondition *metav1.Condition
			for i := range updated.Status.Conditions {
				if updated.Status.Conditions[i].Type == conditionTypeAuthConfigured {
					authCondition = &updated.Status.Conditions[i]
					break
				}
			}
			Expect(authCondition).NotTo(BeNil())
			Expect(authCondition.Status).To(Equal(metav1.ConditionFalse))
			Expect(authCondition.Reason).To(Equal("KuadrantNotFound"))

			Expect(updated.Status.Phase).To(Equal("Active"), "should not block on missing Kuadrant")
		})
	})

	Context("When gateway config ConfigMap is missing", func() {
		BeforeEach(func() {
			deploy := newTestDeployment(deployName, namespace)
			Expect(k8sClient.Create(ctx, deploy)).To(Succeed())
		})

		AfterEach(func() {
			cleanupAgentRuntime(ctx, arName, namespace, reconciler())
			cleanupDeployment(ctx, deployName, namespace)
		})

		It("should still reconcile successfully without AuthPolicy", func() {
			ar := newTestAgentRuntime(arName, namespace, deployName)
			Expect(k8sClient.Create(ctx, ar)).To(Succeed())

			_, err := reconciler().Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: arName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			updated := &agentv1alpha1.AgentRuntime{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: arName, Namespace: namespace}, updated)).To(Succeed())
			Expect(updated.Status.Phase).To(Equal("Active"))
		})
	})

	Context("Gateway config reading", func() {
		It("should read config from ConfigMap", func() {
			cm := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      gatewayConfigMapName,
					Namespace: namespace,
				},
				Data: map[string]string{
					configKeyIssuerURL:        "https://keycloak.example.com/realms/agents",
					configKeyTokenExchangeURL: "https://keycloak.example.com/realms/agents/protocol/openid-connect/token",
					configKeyTrustDomain:      "example.org",
				},
			}
			Expect(k8sClient.Create(ctx, cm)).To(Succeed())
			defer func() {
				_ = k8sClient.Delete(ctx, cm)
			}()

			r := reconciler()
			gwConfig, err := r.readGatewayConfig(ctx, namespace)
			Expect(err).NotTo(HaveOccurred())
			Expect(gwConfig.IssuerURL).To(Equal("https://keycloak.example.com/realms/agents"))
			Expect(gwConfig.TokenExchangeURL).To(Equal("https://keycloak.example.com/realms/agents/protocol/openid-connect/token"))
			Expect(gwConfig.TrustDomain).To(Equal("example.org"))
		})
	})

	Context("AuthPolicy building", func() {
		It("should build correct AuthPolicy with JWT auth and token exchange", func() {
			ar := &agentv1alpha1.AgentRuntime{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "weather-agent",
					Namespace: "team1",
				},
				Spec: agentv1alpha1.AgentRuntimeSpec{
					Type: "agent",
					TargetRef: agentv1alpha1.TargetRef{
						APIVersion: "apps/v1",
						Kind:       "Deployment",
						Name:       "weather-agent",
					},
					Policy: &agentv1alpha1.PolicyConfig{
						AllowedIngressNamespaces: []string{"team1", "kagenti-system"},
					},
				},
			}

			gwConfig := &GatewayConfig{
				IssuerURL:        "https://spire-oidc.kagenti-system/",
				TokenExchangeURL: "https://keycloak.example.com/token",
				TrustDomain:      "example.org",
			}

			ap := buildAuthPolicy(ar, gwConfig)

			Expect(ap.GetKind()).To(Equal("AuthPolicy"))
			Expect(ap.GetAPIVersion()).To(Equal("kuadrant.io/v1"))
			Expect(ap.GetName()).To(Equal("weather-agent-auth"))
			Expect(ap.GetNamespace()).To(Equal("team1"))

			targetRef, _, _ := unstructured.NestedString(ap.Object, "spec", "targetRef", "name")
			Expect(targetRef).To(Equal("weather-agent-route"))

			targetKind, _, _ := unstructured.NestedString(ap.Object, "spec", "targetRef", "kind")
			Expect(targetKind).To(Equal("HTTPRoute"))

			issuer, _, _ := unstructured.NestedString(ap.Object, "spec", "rules", "authentication", "jwt-authn", "jwt", "issuerUrl")
			Expect(issuer).To(Equal("https://spire-oidc.kagenti-system/"))

			predicate, _, _ := unstructured.NestedString(ap.Object, "spec", "rules", "authorization", "namespace-check", "patternMatching", "patterns")
			_ = predicate

			patterns, _, _ := unstructured.NestedSlice(ap.Object, "spec", "rules", "authorization", "namespace-check", "patternMatching", "patterns")
			Expect(patterns).To(HaveLen(1))
			pattern := patterns[0].(map[string]interface{})
			Expect(pattern["predicate"]).To(ContainSubstring("team1"))
			Expect(pattern["predicate"]).To(ContainSubstring("kagenti-system"))

			audience, _, _ := unstructured.NestedString(ap.Object, "spec", "rules", "metadata", "token-exchange", "http", "bodyParameters", "audience", "value")
			Expect(audience).To(Equal("weather-agent"))

			grantType, _, _ := unstructured.NestedString(ap.Object, "spec", "rules", "metadata", "token-exchange", "http", "bodyParameters", "grant_type", "value")
			Expect(grantType).To(Equal("urn:ietf:params:oauth:grant-type:token-exchange"))

			expression, _, _ := unstructured.NestedString(ap.Object, "spec", "rules", "response", "success", "headers", "authorization", "plain", "expression")
			Expect(expression).To(ContainSubstring("auth.metadata.token-exchange.access_token"))
		})

		It("should build AuthPolicy without authorization when no policy is set", func() {
			ar := &agentv1alpha1.AgentRuntime{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "simple-agent",
					Namespace: "default",
				},
				Spec: agentv1alpha1.AgentRuntimeSpec{
					Type: "agent",
					TargetRef: agentv1alpha1.TargetRef{
						APIVersion: "apps/v1",
						Kind:       "Deployment",
						Name:       "simple-agent",
					},
				},
			}

			gwConfig := &GatewayConfig{
				IssuerURL: "https://spire-oidc/",
			}

			ap := buildAuthPolicy(ar, gwConfig)

			_, found, _ := unstructured.NestedMap(ap.Object, "spec", "rules", "authorization")
			Expect(found).To(BeFalse(), "should not have authorization rules when no policy is set")

			_, found, _ = unstructured.NestedMap(ap.Object, "spec", "rules", "metadata")
			Expect(found).To(BeFalse(), "should not have token exchange when no token-exchange-url is configured")
		})

		It("should set authPolicyName in status.gateway", func() {
			deploy := newTestDeployment(deployName, namespace)
			Expect(k8sClient.Create(ctx, deploy)).To(Succeed())
			defer cleanupDeployment(ctx, deployName, namespace)

			ar := newTestAgentRuntime(arName, namespace, deployName)
			Expect(k8sClient.Create(ctx, ar)).To(Succeed())
			defer cleanupAgentRuntime(ctx, arName, namespace, reconciler())

			_, err := reconciler().Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: arName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			updated := &agentv1alpha1.AgentRuntime{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: arName, Namespace: namespace}, updated)).To(Succeed())
			Expect(updated.Status.Gateway).NotTo(BeNil())
			Expect(updated.Status.Gateway.AuthPolicyName).To(Equal(arName + "-auth"))
		})
	})
})
