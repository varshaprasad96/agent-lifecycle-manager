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

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	agentv1alpha1 "github.com/varshaprasad96/agent-lifecycle-manager/api/v1alpha1"
)

const (
	labelType      = "kagenti.io/type"
	labelProtocol  = "protocol.kagenti.io/a2a"
	labelManagedBy = "app.kubernetes.io/managed-by"
	managerName    = "agent-lifecycle-manager"

	spireCsiDriver     = "csi.spiffe.io"
	spireCsiVolumeName = "spiffe-certs"
	spireCsiMountPath  = "/spiffe-certs"
)

func (r *AgentRuntimeReconciler) mutateWorkload(ctx context.Context, ar *agentv1alpha1.AgentRuntime, workload *unstructured.Unstructured) error {
	log := logf.FromContext(ctx)

	changed := false

	if applyWorkloadLabels(workload, ar) {
		changed = true
	}

	if applyPodTemplateLabels(workload, ar) {
		changed = true
	}

	if ensureSpireCsiVolume(workload) {
		changed = true
	}

	if ensureSpireCsiVolumeMount(workload) {
		changed = true
	}

	if ar.Spec.Trace != nil {
		if ensureOtelEnvVars(workload, ar.Spec.Trace, ar.Name) {
			changed = true
		}
	}

	if changed {
		log.Info("updating workload", "kind", workload.GetKind(), "name", workload.GetName())
		return r.Update(ctx, workload)
	}

	return nil
}

func (r *AgentRuntimeReconciler) cleanupWorkload(ctx context.Context, ar *agentv1alpha1.AgentRuntime, workload *unstructured.Unstructured) error {
	log := logf.FromContext(ctx)

	removeWorkloadLabels(workload)
	removePodTemplateLabels(workload)
	removeSpireCsiVolume(workload)
	removeSpireCsiVolumeMount(workload)
	removeOtelEnvVars(workload)

	log.Info("cleaning up workload", "kind", workload.GetKind(), "name", workload.GetName())
	return r.Update(ctx, workload)
}

func applyWorkloadLabels(workload *unstructured.Unstructured, ar *agentv1alpha1.AgentRuntime) bool {
	labels := workload.GetLabels()
	if labels == nil {
		labels = map[string]string{}
	}

	changed := false
	if labels[labelType] != ar.Spec.Type {
		labels[labelType] = ar.Spec.Type
		changed = true
	}
	if labels[labelProtocol] != "true" {
		labels[labelProtocol] = "true"
		changed = true
	}
	if labels[labelManagedBy] != managerName {
		labels[labelManagedBy] = managerName
		changed = true
	}

	if changed {
		workload.SetLabels(labels)
	}
	return changed
}

func removeWorkloadLabels(workload *unstructured.Unstructured) {
	labels := workload.GetLabels()
	if labels == nil {
		return
	}
	delete(labels, labelType)
	delete(labels, labelProtocol)
	delete(labels, labelManagedBy)
	workload.SetLabels(labels)
}

func applyPodTemplateLabels(workload *unstructured.Unstructured, ar *agentv1alpha1.AgentRuntime) bool {
	labels, _, _ := unstructured.NestedStringMap(workload.Object, "spec", "template", "metadata", "labels")
	if labels == nil {
		labels = map[string]string{}
	}

	changed := false
	if labels[labelType] != ar.Spec.Type {
		labels[labelType] = ar.Spec.Type
		changed = true
	}
	if labels[labelProtocol] != "true" {
		labels[labelProtocol] = "true"
		changed = true
	}

	if changed {
		_ = unstructured.SetNestedStringMap(workload.Object, labels, "spec", "template", "metadata", "labels")
	}
	return changed
}

func removePodTemplateLabels(workload *unstructured.Unstructured) {
	labels, _, _ := unstructured.NestedStringMap(workload.Object, "spec", "template", "metadata", "labels")
	if labels == nil {
		return
	}
	delete(labels, labelType)
	delete(labels, labelProtocol)
	_ = unstructured.SetNestedStringMap(workload.Object, labels, "spec", "template", "metadata", "labels")
}

func ensureSpireCsiVolume(workload *unstructured.Unstructured) bool {
	volumes, _, _ := unstructured.NestedSlice(workload.Object, "spec", "template", "spec", "volumes")

	for _, v := range volumes {
		vol, ok := v.(map[string]interface{})
		if !ok {
			continue
		}
		name, _, _ := unstructured.NestedString(vol, "name")
		if name == spireCsiVolumeName {
			return false
		}
	}

	csiVolume := map[string]interface{}{
		"name": spireCsiVolumeName,
		"csi": map[string]interface{}{
			"driver":   spireCsiDriver,
			"readOnly": true,
		},
	}
	volumes = append(volumes, csiVolume)
	_ = unstructured.SetNestedSlice(workload.Object, volumes, "spec", "template", "spec", "volumes")
	return true
}

func removeSpireCsiVolume(workload *unstructured.Unstructured) {
	volumes, _, _ := unstructured.NestedSlice(workload.Object, "spec", "template", "spec", "volumes")
	filtered := make([]interface{}, 0, len(volumes))
	for _, v := range volumes {
		vol, ok := v.(map[string]interface{})
		if !ok {
			continue
		}
		name, _, _ := unstructured.NestedString(vol, "name")
		if name != spireCsiVolumeName {
			filtered = append(filtered, vol)
		}
	}
	_ = unstructured.SetNestedSlice(workload.Object, filtered, "spec", "template", "spec", "volumes")
}

func ensureSpireCsiVolumeMount(workload *unstructured.Unstructured) bool {
	containers, _, _ := unstructured.NestedSlice(workload.Object, "spec", "template", "spec", "containers")
	if len(containers) == 0 {
		return false
	}

	changed := false
	for i, c := range containers {
		container, ok := c.(map[string]interface{})
		if !ok {
			continue
		}

		mounts, _, _ := unstructured.NestedSlice(container, "volumeMounts")
		alreadyMounted := false
		for _, m := range mounts {
			mount, ok := m.(map[string]interface{})
			if !ok {
				continue
			}
			name, _, _ := unstructured.NestedString(mount, "name")
			if name == spireCsiVolumeName {
				alreadyMounted = true
				break
			}
		}

		if !alreadyMounted {
			mount := map[string]interface{}{
				"name":      spireCsiVolumeName,
				"mountPath": spireCsiMountPath,
				"readOnly":  true,
			}
			mounts = append(mounts, mount)
			_ = unstructured.SetNestedSlice(container, mounts, "volumeMounts")
			containers[i] = container
			changed = true
		}
	}

	if changed {
		_ = unstructured.SetNestedSlice(workload.Object, containers, "spec", "template", "spec", "containers")
	}
	return changed
}

func removeSpireCsiVolumeMount(workload *unstructured.Unstructured) {
	containers, _, _ := unstructured.NestedSlice(workload.Object, "spec", "template", "spec", "containers")

	for i, c := range containers {
		container, ok := c.(map[string]interface{})
		if !ok {
			continue
		}

		mounts, _, _ := unstructured.NestedSlice(container, "volumeMounts")
		filtered := make([]interface{}, 0, len(mounts))
		for _, m := range mounts {
			mount, ok := m.(map[string]interface{})
			if !ok {
				continue
			}
			name, _, _ := unstructured.NestedString(mount, "name")
			if name != spireCsiVolumeName {
				filtered = append(filtered, mount)
			}
		}
		_ = unstructured.SetNestedSlice(container, filtered, "volumeMounts")
		containers[i] = container
	}

	_ = unstructured.SetNestedSlice(workload.Object, containers, "spec", "template", "spec", "containers")
}

var otelEnvVarNames = []string{
	"OTEL_EXPORTER_OTLP_ENDPOINT",
	"OTEL_EXPORTER_OTLP_PROTOCOL",
	"OTEL_RESOURCE_ATTRIBUTES",
}

func ensureOtelEnvVars(workload *unstructured.Unstructured, trace *agentv1alpha1.TraceConfig, agentName string) bool {
	containers, _, _ := unstructured.NestedSlice(workload.Object, "spec", "template", "spec", "containers")
	if len(containers) == 0 {
		return false
	}

	desired := map[string]string{
		"OTEL_EXPORTER_OTLP_ENDPOINT": trace.Endpoint,
		"OTEL_EXPORTER_OTLP_PROTOCOL": trace.Protocol,
		"OTEL_RESOURCE_ATTRIBUTES":    fmt.Sprintf("kagenti.agent.name=%s", agentName),
	}

	changed := false
	for i, c := range containers {
		container, ok := c.(map[string]interface{})
		if !ok {
			continue
		}

		envVars, _, _ := unstructured.NestedSlice(container, "env")

		for name, value := range desired {
			found := false
			for j, e := range envVars {
				env, ok := e.(map[string]interface{})
				if !ok {
					continue
				}
				n, _, _ := unstructured.NestedString(env, "name")
				if n == name {
					found = true
					existing, _, _ := unstructured.NestedString(env, "value")
					if existing != value {
						env["value"] = value
						envVars[j] = env
						changed = true
					}
					break
				}
			}
			if !found {
				envVars = append(envVars, map[string]interface{}{
					"name":  name,
					"value": value,
				})
				changed = true
			}
		}

		if changed {
			_ = unstructured.SetNestedSlice(container, envVars, "env")
			containers[i] = container
		}
	}

	if changed {
		_ = unstructured.SetNestedSlice(workload.Object, containers, "spec", "template", "spec", "containers")
	}
	return changed
}

func removeOtelEnvVars(workload *unstructured.Unstructured) {
	containers, _, _ := unstructured.NestedSlice(workload.Object, "spec", "template", "spec", "containers")

	otelNames := make(map[string]bool, len(otelEnvVarNames))
	for _, name := range otelEnvVarNames {
		otelNames[name] = true
	}

	for i, c := range containers {
		container, ok := c.(map[string]interface{})
		if !ok {
			continue
		}

		envVars, _, _ := unstructured.NestedSlice(container, "env")
		filtered := make([]interface{}, 0, len(envVars))
		for _, e := range envVars {
			env, ok := e.(map[string]interface{})
			if !ok {
				continue
			}
			name, _, _ := unstructured.NestedString(env, "name")
			if !otelNames[name] {
				filtered = append(filtered, env)
			}
		}
		_ = unstructured.SetNestedSlice(container, filtered, "env")
		containers[i] = container
	}

	_ = unstructured.SetNestedSlice(workload.Object, containers, "spec", "template", "spec", "containers")
}
