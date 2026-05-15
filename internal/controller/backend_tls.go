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
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	agentv1alpha1 "github.com/varshaprasad96/agent-lifecycle-manager/api/v1alpha1"
)

const (
	conditionTypeIdentityVerified = "IdentityVerified"
	conditionTypeCardFetched      = "CardFetched"

	spireTrustBundleConfigMap = "spire-trust-bundle"
	agentCardPath             = "/.well-known/agent-card.json"
	cardFetchTimeout          = 5 * time.Second
)

var backendTLSPolicyGVK = schema.GroupVersionKind{
	Group:   "gateway.networking.k8s.io",
	Version: "v1",
	Kind:    "BackendTLSPolicy",
}

func backendTLSPolicyName(ar *agentv1alpha1.AgentRuntime) string {
	return ar.Name + "-backend-tls"
}

// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=backendtlspolicies,verbs=get;list;watch;create;update;patch;delete

func (r *AgentRuntimeReconciler) reconcileBackendTLSPolicy(ctx context.Context, ar *agentv1alpha1.AgentRuntime) error {
	log := logf.FromContext(ctx)

	if ar.Spec.Identity == nil || ar.Spec.Identity.SPIFFE == nil {
		log.Info("no SPIFFE identity configured, skipping BackendTLSPolicy")
		return nil
	}

	if !r.isBackendTLSPolicyCRDAvailable(ctx) {
		log.Info("BackendTLSPolicy CRD not found, skipping")
		r.setCondition(ar, conditionTypeIdentityVerified, metav1.ConditionFalse, "BackendTLSPolicyNotAvailable",
			"BackendTLSPolicy CRD not available on this cluster")
		return nil
	}

	desired := buildBackendTLSPolicy(ar)
	if err := ctrl.SetControllerReference(ar, desired, r.Scheme); err != nil {
		return fmt.Errorf("setting owner reference on BackendTLSPolicy: %w", err)
	}

	existing := &unstructured.Unstructured{}
	existing.SetGroupVersionKind(backendTLSPolicyGVK)
	err := r.Get(ctx, types.NamespacedName{Name: desired.GetName(), Namespace: desired.GetNamespace()}, existing)
	if errors.IsNotFound(err) {
		log.Info("creating BackendTLSPolicy", "name", desired.GetName())
		if err := r.Create(ctx, desired); err != nil {
			return fmt.Errorf("creating BackendTLSPolicy: %w", err)
		}
		r.setCondition(ar, conditionTypeIdentityVerified, metav1.ConditionTrue, "BackendTLSPolicyCreated",
			"mTLS configured via BackendTLSPolicy")
		return nil
	}
	if err != nil {
		return fmt.Errorf("getting BackendTLSPolicy: %w", err)
	}

	spec, _, _ := unstructured.NestedMap(desired.Object, "spec")
	_ = unstructured.SetNestedMap(existing.Object, spec, "spec")
	log.Info("updating BackendTLSPolicy", "name", existing.GetName())
	if err := r.Update(ctx, existing); err != nil {
		return fmt.Errorf("updating BackendTLSPolicy: %w", err)
	}

	r.setCondition(ar, conditionTypeIdentityVerified, metav1.ConditionTrue, "BackendTLSPolicyReady",
		"mTLS configured via BackendTLSPolicy")
	return nil
}

func (r *AgentRuntimeReconciler) isBackendTLSPolicyCRDAvailable(ctx context.Context) bool {
	_, err := r.RESTMapper().RESTMapping(
		schema.GroupKind{Group: "gateway.networking.k8s.io", Kind: "BackendTLSPolicy"},
	)
	return err == nil
}

func buildBackendTLSPolicy(ar *agentv1alpha1.AgentRuntime) *unstructured.Unstructured {
	hostname := fmt.Sprintf("%s.%s.svc.cluster.local", ar.Spec.TargetRef.Name, ar.Namespace)

	obj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "gateway.networking.k8s.io/v1",
			"kind":       "BackendTLSPolicy",
			"metadata": map[string]interface{}{
				"name":      backendTLSPolicyName(ar),
				"namespace": ar.Namespace,
				"labels": map[string]interface{}{
					labelManagedBy: managerName,
				},
			},
			"spec": map[string]interface{}{
				"targetRefs": []interface{}{
					map[string]interface{}{
						"group": "",
						"kind":  "Service",
						"name":  ar.Spec.TargetRef.Name,
					},
				},
				"validation": map[string]interface{}{
					"caCertificateRefs": []interface{}{
						map[string]interface{}{
							"group": "",
							"kind":  "ConfigMap",
							"name":  spireTrustBundleConfigMap,
						},
					},
					"hostname": hostname,
				},
			},
		},
	}
	return obj
}

// A2ACard represents the agent card fetched from the A2A endpoint.
type A2ACard struct {
	Name        string     `json:"name"`
	Description string     `json:"description,omitempty"`
	Version     string     `json:"version,omitempty"`
	Skills      []A2ASkill `json:"skills,omitempty"`
}

// A2ASkill represents a skill in the A2A card.
type A2ASkill struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func (r *AgentRuntimeReconciler) fetchAgentCard(ctx context.Context, ar *agentv1alpha1.AgentRuntime) {
	log := logf.FromContext(ctx)

	serviceURL := fmt.Sprintf("http://%s.%s.svc.cluster.local:%d%s",
		ar.Spec.TargetRef.Name, ar.Namespace, defaultAgentPort, agentCardPath)

	httpClient := &http.Client{
		Timeout: cardFetchTimeout,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true, //nolint:gosec // mTLS validation happens at gateway level
			},
		},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, serviceURL, nil)
	if err != nil {
		log.V(1).Info("failed to create card fetch request", "error", err)
		r.setCondition(ar, conditionTypeCardFetched, metav1.ConditionFalse, "FetchError", err.Error())
		return
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		log.V(1).Info("agent card not reachable", "url", serviceURL, "error", err)
		r.setCondition(ar, conditionTypeCardFetched, metav1.ConditionFalse, "AgentUnreachable",
			fmt.Sprintf("Could not reach agent at %s: %v", serviceURL, err))
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.V(1).Info("agent card returned non-200", "status", resp.StatusCode)
		r.setCondition(ar, conditionTypeCardFetched, metav1.ConditionFalse, "CardNotFound",
			fmt.Sprintf("Agent returned HTTP %d for agent card", resp.StatusCode))
		return
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		r.setCondition(ar, conditionTypeCardFetched, metav1.ConditionFalse, "ReadError", err.Error())
		return
	}

	var card A2ACard
	if err := json.Unmarshal(body, &card); err != nil {
		r.setCondition(ar, conditionTypeCardFetched, metav1.ConditionFalse, "ParseError",
			fmt.Sprintf("Failed to parse agent card JSON: %v", err))
		return
	}

	now := metav1.Now()
	skills := make([]string, 0, len(card.Skills))
	for _, s := range card.Skills {
		skills = append(skills, s.Name)
	}

	usedMTLS := resp.TLS != nil

	ar.Status.Card = &agentv1alpha1.CardStatus{
		Name:            card.Name,
		Version:         card.Version,
		Skills:          skills,
		FetchedAt:       &now,
		FetchedOverMTLS: usedMTLS,
	}

	r.setCondition(ar, conditionTypeCardFetched, metav1.ConditionTrue, "CardFetched",
		fmt.Sprintf("Agent card fetched: %s v%s (%d skills)", card.Name, card.Version, len(skills)))

	log.Info("fetched agent card", "name", card.Name, "version", card.Version, "skills", len(skills), "mtls", usedMTLS)
}
