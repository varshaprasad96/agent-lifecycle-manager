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
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	agentv1alpha1 "github.com/varshaprasad96/agent-lifecycle-manager/api/v1alpha1"
)

func newTestDeployment(name, namespace string) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To(int32(1)),
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": name},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"app": name},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "agent",
							Image: "test-agent:latest",
							Ports: []corev1.ContainerPort{{ContainerPort: 8000}},
						},
					},
				},
			},
		},
	}
}

func newTestAgentRuntime(name, namespace, deployName string) *agentv1alpha1.AgentRuntime {
	return &agentv1alpha1.AgentRuntime{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: agentv1alpha1.AgentRuntimeSpec{
			Type: "agent",
			TargetRef: agentv1alpha1.TargetRef{
				APIVersion: "apps/v1",
				Kind:       "Deployment",
				Name:       deployName,
			},
			Identity: &agentv1alpha1.IdentityConfig{
				SPIFFE: &agentv1alpha1.SPIFFEConfig{
					TrustDomain: "example.org",
				},
			},
			Trace: &agentv1alpha1.TraceConfig{
				Endpoint: "otel-collector:4317",
				Protocol: "grpc",
			},
		},
	}
}

var _ = Describe("AgentRuntime Controller", func() {
	const (
		namespace  = "default"
		deployName = "test-agent"
		arName     = "test-agentruntime"
	)

	ctx := context.Background()

	Context("When target workload exists", func() {
		BeforeEach(func() {
			deploy := newTestDeployment(deployName, namespace)
			Expect(k8sClient.Create(ctx, deploy)).To(Succeed())
		})

		AfterEach(func() {
			ar := &agentv1alpha1.AgentRuntime{}
			err := k8sClient.Get(ctx, types.NamespacedName{Name: arName, Namespace: namespace}, ar)
			if err == nil {
				Expect(k8sClient.Delete(ctx, ar)).To(Succeed())

				reconciler := &AgentRuntimeReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
				_, _ = reconciler.Reconcile(ctx, reconcile.Request{
					NamespacedName: types.NamespacedName{Name: arName, Namespace: namespace},
				})
			}

			deploy := &appsv1.Deployment{}
			err = k8sClient.Get(ctx, types.NamespacedName{Name: deployName, Namespace: namespace}, deploy)
			if err == nil {
				Expect(k8sClient.Delete(ctx, deploy)).To(Succeed())
			}
		})

		It("should resolve targetRef and set condition", func() {
			ar := newTestAgentRuntime(arName, namespace, deployName)
			Expect(k8sClient.Create(ctx, ar)).To(Succeed())

			reconciler := &AgentRuntimeReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}

			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: arName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			updated := &agentv1alpha1.AgentRuntime{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: arName, Namespace: namespace}, updated)).To(Succeed())
			Expect(updated.Status.Phase).To(Equal("Active"))

			var targetResolved *metav1.Condition
			for i := range updated.Status.Conditions {
				if updated.Status.Conditions[i].Type == "TargetResolved" {
					targetResolved = &updated.Status.Conditions[i]
					break
				}
			}
			Expect(targetResolved).NotTo(BeNil())
			Expect(targetResolved.Status).To(Equal(metav1.ConditionTrue))
		})

		It("should apply kagenti labels to the workload", func() {
			ar := newTestAgentRuntime(arName, namespace, deployName)
			Expect(k8sClient.Create(ctx, ar)).To(Succeed())

			reconciler := &AgentRuntimeReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: arName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			deploy := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: deployName, Namespace: namespace}, deploy)).To(Succeed())

			Expect(deploy.Labels[labelType]).To(Equal("agent"))
			Expect(deploy.Labels[labelProtocol]).To(Equal("true"))
			Expect(deploy.Labels[labelManagedBy]).To(Equal(managerName))

			Expect(deploy.Spec.Template.Labels[labelType]).To(Equal("agent"))
			Expect(deploy.Spec.Template.Labels[labelProtocol]).To(Equal("true"))
		})

		It("should inject SPIRE CSI volume and volume mount", func() {
			ar := newTestAgentRuntime(arName, namespace, deployName)
			Expect(k8sClient.Create(ctx, ar)).To(Succeed())

			reconciler := &AgentRuntimeReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: arName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			deploy := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: deployName, Namespace: namespace}, deploy)).To(Succeed())

			var foundVolume bool
			for _, v := range deploy.Spec.Template.Spec.Volumes {
				if v.Name == spireCsiVolumeName && v.CSI != nil && v.CSI.Driver == spireCsiDriver {
					foundVolume = true
					break
				}
			}
			Expect(foundVolume).To(BeTrue(), "SPIRE CSI volume should be present")

			var foundMount bool
			for _, vm := range deploy.Spec.Template.Spec.Containers[0].VolumeMounts {
				if vm.Name == spireCsiVolumeName && vm.MountPath == spireCsiMountPath {
					foundMount = true
					break
				}
			}
			Expect(foundMount).To(BeTrue(), "SPIRE CSI volume mount should be present")
		})

		It("should inject OTEL env vars", func() {
			ar := newTestAgentRuntime(arName, namespace, deployName)
			Expect(k8sClient.Create(ctx, ar)).To(Succeed())

			reconciler := &AgentRuntimeReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: arName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			deploy := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: deployName, Namespace: namespace}, deploy)).To(Succeed())

			envMap := map[string]string{}
			for _, e := range deploy.Spec.Template.Spec.Containers[0].Env {
				envMap[e.Name] = e.Value
			}
			Expect(envMap["OTEL_EXPORTER_OTLP_ENDPOINT"]).To(Equal("otel-collector:4317"))
			Expect(envMap["OTEL_EXPORTER_OTLP_PROTOCOL"]).To(Equal("grpc"))
			Expect(envMap["OTEL_RESOURCE_ATTRIBUTES"]).To(Equal("kagenti.agent.name=test-agentruntime"))
		})

		It("should set SPIFFE ID in status", func() {
			ar := newTestAgentRuntime(arName, namespace, deployName)
			Expect(k8sClient.Create(ctx, ar)).To(Succeed())

			reconciler := &AgentRuntimeReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: arName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			updated := &agentv1alpha1.AgentRuntime{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: arName, Namespace: namespace}, updated)).To(Succeed())
			Expect(updated.Status.Identity).NotTo(BeNil())
			Expect(updated.Status.Identity.SpiffeID).To(Equal("spiffe://example.org/ns/default/sa/default"))
			Expect(updated.Status.Identity.CertificateSource).To(Equal("spire"))
		})

		It("should be idempotent on re-reconcile", func() {
			ar := newTestAgentRuntime(arName, namespace, deployName)
			Expect(k8sClient.Create(ctx, ar)).To(Succeed())

			reconciler := &AgentRuntimeReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			nn := types.NamespacedName{Name: arName, Namespace: namespace}

			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())

			_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())

			deploy := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: deployName, Namespace: namespace}, deploy)).To(Succeed())

			csiCount := 0
			for _, v := range deploy.Spec.Template.Spec.Volumes {
				if v.Name == spireCsiVolumeName {
					csiCount++
				}
			}
			Expect(csiCount).To(Equal(1), "SPIRE CSI volume should not be duplicated")
		})

		It("should clean up workload on AgentRuntime deletion", func() {
			ar := newTestAgentRuntime(arName, namespace, deployName)
			Expect(k8sClient.Create(ctx, ar)).To(Succeed())

			reconciler := &AgentRuntimeReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			nn := types.NamespacedName{Name: arName, Namespace: namespace}

			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())

			deploy := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: deployName, Namespace: namespace}, deploy)).To(Succeed())
			Expect(deploy.Labels[labelType]).To(Equal("agent"))

			Expect(k8sClient.Delete(ctx, ar)).To(Succeed())

			_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: deployName, Namespace: namespace}, deploy)).To(Succeed())
			Expect(deploy.Labels).NotTo(HaveKey(labelType))
			Expect(deploy.Labels).NotTo(HaveKey(labelProtocol))
			Expect(deploy.Labels).NotTo(HaveKey(labelManagedBy))

			var foundCsi bool
			for _, v := range deploy.Spec.Template.Spec.Volumes {
				if v.Name == spireCsiVolumeName {
					foundCsi = true
				}
			}
			Expect(foundCsi).To(BeFalse(), "SPIRE CSI volume should be removed after cleanup")
		})
	})

	Context("When target workload does not exist", func() {
		It("should set error condition", func() {
			ar := newTestAgentRuntime(arName, namespace, "nonexistent-deploy")
			Expect(k8sClient.Create(ctx, ar)).To(Succeed())

			reconciler := &AgentRuntimeReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: arName, Namespace: namespace},
			})
			Expect(err).To(HaveOccurred())

			updated := &agentv1alpha1.AgentRuntime{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: arName, Namespace: namespace}, updated)).To(Succeed())
			Expect(updated.Status.Phase).To(Equal("Error"))

			var targetResolved *metav1.Condition
			for i := range updated.Status.Conditions {
				if updated.Status.Conditions[i].Type == "TargetResolved" {
					targetResolved = &updated.Status.Conditions[i]
					break
				}
			}
			Expect(targetResolved).NotTo(BeNil())
			Expect(targetResolved.Status).To(Equal(metav1.ConditionFalse))
			Expect(targetResolved.Reason).To(Equal("TargetNotFound"))

			Expect(k8sClient.Delete(ctx, updated)).To(Succeed())
			_, _ = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: arName, Namespace: namespace},
			})
		})
	})

	Context("When AgentRuntime is deleted and workload is gone", func() {
		It("should complete deletion without error", func() {
			ar := newTestAgentRuntime(arName, namespace, deployName)
			Expect(k8sClient.Create(ctx, ar)).To(Succeed())

			reconciler := &AgentRuntimeReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			nn := types.NamespacedName{Name: arName, Namespace: namespace}

			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).To(HaveOccurred())

			Expect(k8sClient.Delete(ctx, ar)).To(Succeed())

			_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())

			err = k8sClient.Get(ctx, nn, &agentv1alpha1.AgentRuntime{})
			Expect(errors.IsNotFound(err)).To(BeTrue())
		})
	})
})
