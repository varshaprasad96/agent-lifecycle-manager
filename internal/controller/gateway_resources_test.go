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
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	agentv1alpha1 "github.com/varshaprasad96/agent-lifecycle-manager/api/v1alpha1"
)

var _ = Describe("Gateway Resources", func() {
	const (
		namespace  = "default"
		deployName = "gw-test-agent"
		arName     = "gw-test-agentruntime"
	)

	ctx := context.Background()
	reconciler := func() *AgentRuntimeReconciler {
		return &AgentRuntimeReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
	}

	Context("HTTPRoute generation", func() {
		BeforeEach(func() {
			deploy := newTestDeployment(deployName, namespace)
			Expect(k8sClient.Create(ctx, deploy)).To(Succeed())
		})

		AfterEach(func() {
			cleanupAgentRuntime(ctx, arName, namespace, reconciler())
			cleanupDeployment(ctx, deployName, namespace)
		})

		It("should create an HTTPRoute owned by AgentRuntime", func() {
			ar := newTestAgentRuntime(arName, namespace, deployName)
			Expect(k8sClient.Create(ctx, ar)).To(Succeed())

			_, err := reconciler().Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: arName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			route := &gatewayv1.HTTPRoute{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      arName + "-route",
				Namespace: namespace,
			}, route)).To(Succeed())

			Expect(route.Spec.ParentRefs).To(HaveLen(1))
			Expect(string(route.Spec.ParentRefs[0].Name)).To(Equal(defaultGatewayName))

			Expect(route.Spec.Hostnames).To(HaveLen(1))
			Expect(string(route.Spec.Hostnames[0])).To(Equal(arName + "." + namespace + ".agents.local"))

			Expect(route.Spec.Rules).To(HaveLen(1))
			Expect(route.Spec.Rules[0].BackendRefs).To(HaveLen(1))
			Expect(string(route.Spec.Rules[0].BackendRefs[0].Name)).To(Equal(deployName))

			Expect(route.OwnerReferences).To(HaveLen(1))
			Expect(route.OwnerReferences[0].Kind).To(Equal("AgentRuntime"))
		})

		It("should set GatewayConfigured condition and status.gateway", func() {
			ar := newTestAgentRuntime(arName, namespace, deployName)
			Expect(k8sClient.Create(ctx, ar)).To(Succeed())

			_, err := reconciler().Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: arName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			updated := &agentv1alpha1.AgentRuntime{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: arName, Namespace: namespace}, updated)).To(Succeed())

			var gwConfigured *metav1.Condition
			for i := range updated.Status.Conditions {
				if updated.Status.Conditions[i].Type == conditionTypeGatewayConfigured {
					gwConfigured = &updated.Status.Conditions[i]
					break
				}
			}
			Expect(gwConfigured).NotTo(BeNil())
			Expect(gwConfigured.Status).To(Equal(metav1.ConditionTrue))

			Expect(updated.Status.Gateway).NotTo(BeNil())
			Expect(updated.Status.Gateway.HTTPRouteName).To(Equal(arName + "-route"))
			Expect(updated.Status.Gateway.GatewayEndpoint).To(ContainSubstring(arName))
		})

		It("should update HTTPRoute on re-reconcile", func() {
			ar := newTestAgentRuntime(arName, namespace, deployName)
			Expect(k8sClient.Create(ctx, ar)).To(Succeed())

			nn := types.NamespacedName{Name: arName, Namespace: namespace}
			_, err := reconciler().Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())

			_, err = reconciler().Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())

			routes := &gatewayv1.HTTPRouteList{}
			Expect(k8sClient.List(ctx, routes)).To(Succeed())

			count := 0
			for _, r := range routes.Items {
				if r.Name == arName+"-route" {
					count++
				}
			}
			Expect(count).To(Equal(1), "should not duplicate HTTPRoute")
		})
	})

	Context("NetworkPolicy generation", func() {
		BeforeEach(func() {
			deploy := newTestDeployment(deployName, namespace)
			Expect(k8sClient.Create(ctx, deploy)).To(Succeed())
		})

		AfterEach(func() {
			cleanupAgentRuntime(ctx, arName, namespace, reconciler())
			cleanupDeployment(ctx, deployName, namespace)
		})

		It("should create NetworkPolicy when spec.policy is set", func() {
			ar := newTestAgentRuntime(arName, namespace, deployName)
			ar.Spec.Policy = &agentv1alpha1.PolicyConfig{
				AllowedIngressNamespaces: []string{"team1", "kagenti-system"},
				Dependencies: []agentv1alpha1.Dependency{
					{Name: "weather-tool"},
				},
				ExternalEgress: []agentv1alpha1.ExternalEgressRule{
					{CIDR: "10.0.0.0/8", Port: 443},
				},
			}
			Expect(k8sClient.Create(ctx, ar)).To(Succeed())

			_, err := reconciler().Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: arName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			np := &networkingv1.NetworkPolicy{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      arName + "-netpol",
				Namespace: namespace,
			}, np)).To(Succeed())

			Expect(np.Spec.PolicyTypes).To(ContainElement(networkingv1.PolicyTypeIngress))
			Expect(np.Spec.PolicyTypes).To(ContainElement(networkingv1.PolicyTypeEgress))

			Expect(np.Spec.Ingress).To(HaveLen(1))
			Expect(np.Spec.Ingress[0].From).To(HaveLen(3))

			hasGatewayPeer := false
			for _, peer := range np.Spec.Ingress[0].From {
				if peer.PodSelector != nil && peer.PodSelector.MatchLabels[gatewayAppLabel] == gatewayAppValue {
					hasGatewayPeer = true
				}
			}
			Expect(hasGatewayPeer).To(BeTrue(), "ingress should allow gateway pods")

			Expect(len(np.Spec.Egress)).To(BeNumerically(">=", 3))

			hasGatewayEgress := false
			for _, rule := range np.Spec.Egress {
				for _, peer := range rule.To {
					if peer.PodSelector != nil && peer.PodSelector.MatchLabels[gatewayAppLabel] == gatewayAppValue {
						hasGatewayEgress = true
					}
				}
			}
			Expect(hasGatewayEgress).To(BeTrue(), "egress should allow gateway pods")

			hasCidrEgress := false
			for _, rule := range np.Spec.Egress {
				for _, peer := range rule.To {
					if peer.IPBlock != nil && peer.IPBlock.CIDR == "10.0.0.0/8" {
						hasCidrEgress = true
					}
				}
			}
			Expect(hasCidrEgress).To(BeTrue(), "egress should allow declared CIDR")

			Expect(np.OwnerReferences).To(HaveLen(1))
			Expect(np.OwnerReferences[0].Kind).To(Equal("AgentRuntime"))
		})

		It("should NOT create NetworkPolicy when spec.policy is nil", func() {
			ar := newTestAgentRuntime(arName, namespace, deployName)
			ar.Spec.Policy = nil
			Expect(k8sClient.Create(ctx, ar)).To(Succeed())

			_, err := reconciler().Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: arName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			np := &networkingv1.NetworkPolicy{}
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name:      arName + "-netpol",
				Namespace: namespace,
			}, np)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("not found"))
		})

		It("should set PolicyApplied condition when policy is set", func() {
			ar := newTestAgentRuntime(arName, namespace, deployName)
			ar.Spec.Policy = &agentv1alpha1.PolicyConfig{
				AllowedIngressNamespaces: []string{"default"},
			}
			Expect(k8sClient.Create(ctx, ar)).To(Succeed())

			_, err := reconciler().Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: arName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			updated := &agentv1alpha1.AgentRuntime{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: arName, Namespace: namespace}, updated)).To(Succeed())

			var policyApplied *metav1.Condition
			for i := range updated.Status.Conditions {
				if updated.Status.Conditions[i].Type == conditionTypePolicyApplied {
					policyApplied = &updated.Status.Conditions[i]
					break
				}
			}
			Expect(policyApplied).NotTo(BeNil())
			Expect(policyApplied.Status).To(Equal(metav1.ConditionTrue))

			Expect(updated.Status.Gateway).NotTo(BeNil())
			Expect(updated.Status.Gateway.NetworkPolicyName).To(Equal(arName + "-netpol"))
		})

		It("should delete NetworkPolicy when spec.policy is removed", func() {
			ar := newTestAgentRuntime(arName, namespace, deployName)
			ar.Spec.Policy = &agentv1alpha1.PolicyConfig{
				AllowedIngressNamespaces: []string{"default"},
			}
			Expect(k8sClient.Create(ctx, ar)).To(Succeed())

			nn := types.NamespacedName{Name: arName, Namespace: namespace}
			_, err := reconciler().Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())

			np := &networkingv1.NetworkPolicy{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: arName + "-netpol", Namespace: namespace}, np)).To(Succeed())

			updated := &agentv1alpha1.AgentRuntime{}
			Expect(k8sClient.Get(ctx, nn, updated)).To(Succeed())
			updated.Spec.Policy = nil
			Expect(k8sClient.Update(ctx, updated)).To(Succeed())

			_, err = reconciler().Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())

			err = k8sClient.Get(ctx, types.NamespacedName{Name: arName + "-netpol", Namespace: namespace}, np)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("not found"))
		})
	})
})

func cleanupAgentRuntime(ctx context.Context, name, namespace string, reconciler *AgentRuntimeReconciler) {
	ar := &agentv1alpha1.AgentRuntime{}
	nn := types.NamespacedName{Name: name, Namespace: namespace}
	if err := k8sClient.Get(ctx, nn, ar); err == nil {
		_ = k8sClient.Delete(ctx, ar)
		_, _ = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
	}
}

func cleanupDeployment(ctx context.Context, name, namespace string) {
	deploy := newTestDeployment(name, namespace)
	deploy.Name = name
	deploy.Namespace = namespace
	_ = k8sClient.Delete(ctx, deploy)
}
