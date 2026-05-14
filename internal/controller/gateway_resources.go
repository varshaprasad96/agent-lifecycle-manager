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

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	agentv1alpha1 "github.com/varshaprasad96/agent-lifecycle-manager/api/v1alpha1"
)

const (
	conditionTypeGatewayConfigured = "GatewayConfigured"

	defaultGatewayName = "agent-gateway"
	defaultAgentPort   = 8000
)

func httpRouteName(ar *agentv1alpha1.AgentRuntime) string {
	return ar.Name + "-route"
}

func (r *AgentRuntimeReconciler) reconcileHTTPRoute(ctx context.Context, ar *agentv1alpha1.AgentRuntime) error {
	log := logf.FromContext(ctx)

	desired := r.buildHTTPRoute(ar)
	if err := ctrl.SetControllerReference(ar, desired, r.Scheme); err != nil {
		return fmt.Errorf("setting owner reference on HTTPRoute: %w", err)
	}

	existing := &gatewayv1.HTTPRoute{}
	err := r.Get(ctx, types.NamespacedName{Name: desired.Name, Namespace: desired.Namespace}, existing)
	if errors.IsNotFound(err) {
		log.Info("creating HTTPRoute", "name", desired.Name)
		return r.Create(ctx, desired)
	}
	if err != nil {
		return fmt.Errorf("getting HTTPRoute: %w", err)
	}

	existing.Spec = desired.Spec
	log.Info("updating HTTPRoute", "name", existing.Name)
	return r.Update(ctx, existing)
}

func (r *AgentRuntimeReconciler) buildHTTPRoute(ar *agentv1alpha1.AgentRuntime) *gatewayv1.HTTPRoute {
	namespace := gatewayv1.Namespace(ar.Namespace)
	port := gatewayv1.PortNumber(defaultAgentPort)
	pathType := gatewayv1.PathMatchPathPrefix
	pathValue := "/"
	hostname := gatewayv1.Hostname(fmt.Sprintf("%s.%s.agents.local", ar.Name, ar.Namespace))

	return &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      httpRouteName(ar),
			Namespace: ar.Namespace,
			Labels: map[string]string{
				labelManagedBy: managerName,
				labelType:      ar.Spec.Type,
			},
		},
		Spec: gatewayv1.HTTPRouteSpec{
			CommonRouteSpec: gatewayv1.CommonRouteSpec{
				ParentRefs: []gatewayv1.ParentReference{
					{
						Name:      gatewayv1.ObjectName(defaultGatewayName),
						Namespace: &namespace,
					},
				},
			},
			Hostnames: []gatewayv1.Hostname{hostname},
			Rules: []gatewayv1.HTTPRouteRule{
				{
					Matches: []gatewayv1.HTTPRouteMatch{
						{
							Path: &gatewayv1.HTTPPathMatch{
								Type:  &pathType,
								Value: &pathValue,
							},
						},
					},
					BackendRefs: []gatewayv1.HTTPBackendRef{
						{
							BackendRef: gatewayv1.BackendRef{
								BackendObjectReference: gatewayv1.BackendObjectReference{
									Name: gatewayv1.ObjectName(ar.Spec.TargetRef.Name),
									Port: &port,
								},
							},
						},
					},
				},
			},
		},
	}
}

func gatewayEndpoint(ar *agentv1alpha1.AgentRuntime) string {
	return fmt.Sprintf("https://%s.%s.agents.local", ar.Name, ar.Namespace)
}
