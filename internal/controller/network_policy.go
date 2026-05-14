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
	"net"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	agentv1alpha1 "github.com/varshaprasad96/agent-lifecycle-manager/api/v1alpha1"
)

const (
	conditionTypePolicyApplied = "PolicyApplied"
	gatewayAppLabel            = "app"
	gatewayAppValue            = "agent-gateway"
)

func networkPolicyName(ar *agentv1alpha1.AgentRuntime) string {
	return ar.Name + "-netpol"
}

func (r *AgentRuntimeReconciler) reconcileNetworkPolicy(ctx context.Context, ar *agentv1alpha1.AgentRuntime) error {
	log := logf.FromContext(ctx)

	if ar.Spec.Policy == nil {
		return r.deleteNetworkPolicyIfExists(ctx, ar)
	}

	desired, err := r.buildNetworkPolicy(ar)
	if err != nil {
		return fmt.Errorf("building NetworkPolicy: %w", err)
	}

	if err := ctrl.SetControllerReference(ar, desired, r.Scheme); err != nil {
		return fmt.Errorf("setting owner reference on NetworkPolicy: %w", err)
	}

	existing := &networkingv1.NetworkPolicy{}
	err = r.Get(ctx, types.NamespacedName{Name: desired.Name, Namespace: desired.Namespace}, existing)
	if errors.IsNotFound(err) {
		log.Info("creating NetworkPolicy", "name", desired.Name)
		return r.Create(ctx, desired)
	}
	if err != nil {
		return fmt.Errorf("getting NetworkPolicy: %w", err)
	}

	existing.Spec = desired.Spec
	log.Info("updating NetworkPolicy", "name", existing.Name)
	return r.Update(ctx, existing)
}

func (r *AgentRuntimeReconciler) deleteNetworkPolicyIfExists(ctx context.Context, ar *agentv1alpha1.AgentRuntime) error {
	existing := &networkingv1.NetworkPolicy{}
	err := r.Get(ctx, types.NamespacedName{Name: networkPolicyName(ar), Namespace: ar.Namespace}, existing)
	if errors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return r.Delete(ctx, existing)
}

func (r *AgentRuntimeReconciler) buildNetworkPolicy(ar *agentv1alpha1.AgentRuntime) (*networkingv1.NetworkPolicy, error) {
	policy := ar.Spec.Policy

	workloadLabels := map[string]string{
		"app": ar.Spec.TargetRef.Name,
	}

	np := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      networkPolicyName(ar),
			Namespace: ar.Namespace,
			Labels: map[string]string{
				labelManagedBy: managerName,
			},
		},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{
				MatchLabels: workloadLabels,
			},
			PolicyTypes: []networkingv1.PolicyType{},
		},
	}

	if len(policy.AllowedIngressNamespaces) > 0 {
		np.Spec.PolicyTypes = append(np.Spec.PolicyTypes, networkingv1.PolicyTypeIngress)
		np.Spec.Ingress = buildIngressRules(policy.AllowedIngressNamespaces)
	}

	if len(policy.Dependencies) > 0 || len(policy.ExternalEgress) > 0 {
		np.Spec.PolicyTypes = append(np.Spec.PolicyTypes, networkingv1.PolicyTypeEgress)
		egressRules, err := buildEgressRules(policy, ar.Namespace)
		if err != nil {
			return nil, err
		}
		np.Spec.Egress = egressRules
	}

	return np, nil
}

func buildIngressRules(namespaces []string) []networkingv1.NetworkPolicyIngressRule {
	var peers []networkingv1.NetworkPolicyPeer

	peers = append(peers, networkingv1.NetworkPolicyPeer{
		PodSelector: &metav1.LabelSelector{
			MatchLabels: map[string]string{
				gatewayAppLabel: gatewayAppValue,
			},
		},
	})

	for _, ns := range namespaces {
		peers = append(peers, networkingv1.NetworkPolicyPeer{
			NamespaceSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"kubernetes.io/metadata.name": ns,
				},
			},
		})
	}

	return []networkingv1.NetworkPolicyIngressRule{
		{From: peers},
	}
}

func buildEgressRules(policy *agentv1alpha1.PolicyConfig, namespace string) ([]networkingv1.NetworkPolicyEgressRule, error) {
	var rules []networkingv1.NetworkPolicyEgressRule

	rules = append(rules, networkingv1.NetworkPolicyEgressRule{
		To: []networkingv1.NetworkPolicyPeer{
			{
				PodSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						gatewayAppLabel: gatewayAppValue,
					},
				},
			},
		},
	})

	dnsPort := intstr.FromInt32(53)
	rules = append(rules, networkingv1.NetworkPolicyEgressRule{
		Ports: []networkingv1.NetworkPolicyPort{
			{Port: &dnsPort, Protocol: protocolPtr(corev1.ProtocolUDP)},
			{Port: &dnsPort, Protocol: protocolPtr(corev1.ProtocolTCP)},
		},
	})

	for _, ext := range policy.ExternalEgress {
		rule, err := buildExternalEgressRule(ext)
		if err != nil {
			return nil, err
		}
		rules = append(rules, rule)
	}

	return rules, nil
}

func buildExternalEgressRule(ext agentv1alpha1.ExternalEgressRule) (networkingv1.NetworkPolicyEgressRule, error) {
	port := intstr.FromInt32(ext.Port)
	rule := networkingv1.NetworkPolicyEgressRule{
		Ports: []networkingv1.NetworkPolicyPort{
			{Port: &port, Protocol: protocolPtr(corev1.ProtocolTCP)},
		},
	}

	if ext.CIDR != "" {
		rule.To = []networkingv1.NetworkPolicyPeer{
			{
				IPBlock: &networkingv1.IPBlock{
					CIDR: ext.CIDR,
				},
			},
		}
	} else if ext.Host != "" {
		ips, err := net.LookupHost(ext.Host)
		if err != nil {
			return rule, fmt.Errorf("resolving host %q: %w", ext.Host, err)
		}

		var peers []networkingv1.NetworkPolicyPeer
		for _, ip := range ips {
			cidr := ip + "/32"
			if net.ParseIP(ip).To4() == nil {
				cidr = ip + "/128"
			}
			peers = append(peers, networkingv1.NetworkPolicyPeer{
				IPBlock: &networkingv1.IPBlock{CIDR: cidr},
			})
		}
		rule.To = peers
	}

	return rule, nil
}

func protocolPtr(p corev1.Protocol) *corev1.Protocol {
	return &p
}
