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

	appsv1 "k8s.io/api/apps/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	agentv1alpha1 "github.com/varshaprasad96/agent-lifecycle-manager/api/v1alpha1"
)

const (
	finalizerName = "agentruntime.kagenti.dev/cleanup"

	conditionTypeTargetResolved = "TargetResolved"
	conditionTypeReady          = "Ready"
)

// AgentRuntimeReconciler reconciles a AgentRuntime object
type AgentRuntimeReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=agent.kagenti.dev,resources=agentruntimes,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=agent.kagenti.dev,resources=agentruntimes/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=agent.kagenti.dev,resources=agentruntimes/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=apps,resources=deployments/status,verbs=get
// +kubebuilder:rbac:groups=apps,resources=statefulsets/status,verbs=get
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=httproutes,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=networking.k8s.io,resources=networkpolicies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kuadrant.io,resources=authpolicies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch

func (r *AgentRuntimeReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	ar := &agentv1alpha1.AgentRuntime{}
	if err := r.Get(ctx, req.NamespacedName, ar); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	if !ar.DeletionTimestamp.IsZero() {
		return r.handleDeletion(ctx, ar)
	}

	if !controllerutil.ContainsFinalizer(ar, finalizerName) {
		controllerutil.AddFinalizer(ar, finalizerName)
		if err := r.Update(ctx, ar); err != nil {
			return ctrl.Result{}, err
		}
	}

	workload, err := r.resolveTargetRef(ctx, ar)
	if err != nil {
		log.Error(err, "failed to resolve targetRef")
		r.setCondition(ar, conditionTypeTargetResolved, metav1.ConditionFalse, "TargetNotFound", err.Error())
		r.setCondition(ar, conditionTypeReady, metav1.ConditionFalse, "TargetNotFound", "Target workload not found")
		ar.Status.Phase = "Error"
		_ = r.Status().Update(ctx, ar)
		return ctrl.Result{}, err
	}

	r.setCondition(ar, conditionTypeTargetResolved, metav1.ConditionTrue, "TargetFound",
		fmt.Sprintf("%s/%s found", ar.Spec.TargetRef.Kind, ar.Spec.TargetRef.Name))

	if err := r.mutateWorkload(ctx, ar, workload); err != nil {
		log.Error(err, "failed to mutate workload")
		ar.Status.Phase = "Error"
		r.setCondition(ar, conditionTypeReady, metav1.ConditionFalse, "MutationFailed", err.Error())
		_ = r.Status().Update(ctx, ar)
		return ctrl.Result{}, err
	}

	if err := r.reconcileHTTPRoute(ctx, ar); err != nil {
		log.Error(err, "failed to reconcile HTTPRoute")
		r.setCondition(ar, conditionTypeGatewayConfigured, metav1.ConditionFalse, "HTTPRouteError", err.Error())
		ar.Status.Phase = "Error"
		_ = r.Status().Update(ctx, ar)
		return ctrl.Result{}, err
	}
	r.setCondition(ar, conditionTypeGatewayConfigured, metav1.ConditionTrue, "HTTPRouteReady",
		fmt.Sprintf("HTTPRoute %s created", httpRouteName(ar)))

	if err := r.reconcileNetworkPolicy(ctx, ar); err != nil {
		log.Error(err, "failed to reconcile NetworkPolicy")
		r.setCondition(ar, conditionTypePolicyApplied, metav1.ConditionFalse, "NetworkPolicyError", err.Error())
		ar.Status.Phase = "Error"
		_ = r.Status().Update(ctx, ar)
		return ctrl.Result{}, err
	}
	if ar.Spec.Policy != nil {
		r.setCondition(ar, conditionTypePolicyApplied, metav1.ConditionTrue, "NetworkPolicyCreated",
			fmt.Sprintf("NetworkPolicy %s created", networkPolicyName(ar)))
	}

	if err := r.reconcileAuthPolicy(ctx, ar); err != nil {
		log.Error(err, "failed to reconcile AuthPolicy")
		r.setCondition(ar, conditionTypeAuthConfigured, metav1.ConditionFalse, "AuthPolicyError", err.Error())
		ar.Status.Phase = "Error"
		_ = r.Status().Update(ctx, ar)
		return ctrl.Result{}, err
	}

	if err := r.reconcileBackendTLSPolicy(ctx, ar); err != nil {
		log.Error(err, "failed to reconcile BackendTLSPolicy")
		r.setCondition(ar, conditionTypeIdentityVerified, metav1.ConditionFalse, "BackendTLSError", err.Error())
		ar.Status.Phase = "Error"
		_ = r.Status().Update(ctx, ar)
		return ctrl.Result{}, err
	}

	r.fetchAgentCard(ctx, ar)

	r.updateStatus(ctx, ar, workload)

	if err := r.Status().Update(ctx, ar); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

func (r *AgentRuntimeReconciler) resolveTargetRef(ctx context.Context, ar *agentv1alpha1.AgentRuntime) (*unstructured.Unstructured, error) {
	ref := ar.Spec.TargetRef
	gvk, err := parseGVK(ref.APIVersion, ref.Kind)
	if err != nil {
		return nil, fmt.Errorf("invalid targetRef: %w", err)
	}

	workload := &unstructured.Unstructured{}
	workload.SetGroupVersionKind(gvk)

	key := client.ObjectKey{
		Namespace: ar.Namespace,
		Name:      ref.Name,
	}
	if err := r.Get(ctx, key, workload); err != nil {
		return nil, fmt.Errorf("workload %s/%s not found: %w", ref.Kind, ref.Name, err)
	}

	return workload, nil
}

func (r *AgentRuntimeReconciler) handleDeletion(ctx context.Context, ar *agentv1alpha1.AgentRuntime) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	if !controllerutil.ContainsFinalizer(ar, finalizerName) {
		return ctrl.Result{}, nil
	}

	workload, err := r.resolveTargetRef(ctx, ar)
	if err != nil {
		if !errors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
		log.Info("target workload already deleted, skipping cleanup")
	} else {
		if err := r.cleanupWorkload(ctx, ar, workload); err != nil {
			return ctrl.Result{}, err
		}
	}

	controllerutil.RemoveFinalizer(ar, finalizerName)
	if err := r.Update(ctx, ar); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

func (r *AgentRuntimeReconciler) updateStatus(ctx context.Context, ar *agentv1alpha1.AgentRuntime, workload *unstructured.Unstructured) {
	ar.Status.Phase = "Active"
	ar.Status.ObservedGeneration = ar.Generation

	replicas, found, _ := unstructured.NestedInt64(workload.Object, "status", "readyReplicas")
	if found {
		ar.Status.ConfiguredPods = int32(replicas)
	}

	if ar.Spec.Identity != nil && ar.Spec.Identity.SPIFFE != nil {
		sa := getServiceAccountName(workload)
		ar.Status.Identity = &agentv1alpha1.IdentityStatus{
			SpiffeID:          fmt.Sprintf("spiffe://%s/ns/%s/sa/%s", ar.Spec.Identity.SPIFFE.TrustDomain, ar.Namespace, sa),
			MTLSEnabled:       false,
			CertificateSource: "spire",
		}
	}

	gw := &agentv1alpha1.GatewayStatus{
		HTTPRouteName:   httpRouteName(ar),
		AuthPolicyName:  authPolicyName(ar),
		GatewayEndpoint: gatewayEndpoint(ar),
	}
	if ar.Spec.Policy != nil {
		gw.NetworkPolicyName = networkPolicyName(ar)
	}
	ar.Status.Gateway = gw

	r.setCondition(ar, conditionTypeReady, metav1.ConditionTrue, "WorkloadConfigured", "Agent workload configured successfully")
}

func (r *AgentRuntimeReconciler) setCondition(ar *agentv1alpha1.AgentRuntime, condType string, status metav1.ConditionStatus, reason, message string) {
	meta.SetStatusCondition(&ar.Status.Conditions, metav1.Condition{
		Type:               condType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: ar.Generation,
	})
}

func parseGVK(apiVersion, kind string) (schema.GroupVersionKind, error) {
	gv, err := schema.ParseGroupVersion(apiVersion)
	if err != nil {
		return schema.GroupVersionKind{}, err
	}
	return gv.WithKind(kind), nil
}

func getServiceAccountName(workload *unstructured.Unstructured) string {
	sa, found, _ := unstructured.NestedString(workload.Object, "spec", "template", "spec", "serviceAccountName")
	if found && sa != "" {
		return sa
	}
	return "default"
}

// SetupWithManager sets up the controller with the Manager.
func (r *AgentRuntimeReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&agentv1alpha1.AgentRuntime{}).
		Owns(&appsv1.Deployment{}).
		Owns(&gatewayv1.HTTPRoute{}).
		Owns(&networkingv1.NetworkPolicy{}).
		Named("agentruntime").
		Complete(r)
}
