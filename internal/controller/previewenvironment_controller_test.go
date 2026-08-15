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
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	previewv1alpha1 "github.com/AyoubJedidi/preview-operator/api/v1alpha1"
)

type MockGitHubClient struct {
	LastComment string
	Owner       string
	Repo        string
	PRNumber    int
}

func (m *MockGitHubClient) PostPRComment(ctx context.Context, owner, repo string, prNumber int, comment string) error {
	m.LastComment = comment
	m.Owner = owner
	m.Repo = repo
	m.PRNumber = prNumber
	return nil
}

var _ = Describe("PreviewEnvironment Controller", func() {
	Context("When reconciling a resource", func() {
		const (
			resourceName      = "test-resource"
			resourceNamespace = "default"
		)

		ctx := context.Background()

		typeNamespacedName := types.NamespacedName{
			Name:      resourceName,
			Namespace: resourceNamespace,
		}
		previewenvironment := &previewv1alpha1.PreviewEnvironment{}

		BeforeEach(func() {
			By("creating the custom resource for the Kind PreviewEnvironment")
			err := k8sClient.Get(ctx, typeNamespacedName, previewenvironment)
			if err != nil && errors.IsNotFound(err) {
				resource := &previewv1alpha1.PreviewEnvironment{
					ObjectMeta: metav1.ObjectMeta{
						Name:      resourceName,
						Namespace: resourceNamespace,
					},
					Spec: previewv1alpha1.PreviewEnvironmentSpec{
						PRNumber:  142,
						RepoOwner: "AyoubJedidi",
						RepoName:  "my-microservices-app",
						Branch:    "feature/payment-gateway",
						CommitSha: "7a8b9c0",
						Domain:    "preview.company.com",
						GitOps: previewv1alpha1.GitOpsConfig{
							TargetRevision: "feature/payment-gateway",
							Path:           "k8s/overlays/preview",
							HelmValues: map[string]string{
								"env": "preview",
								"pr":  "142",
							},
						},
					},
				}
				Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			}

			By("creating the argocd namespace if not exists")
			argocdNamespace := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "argocd",
				},
			}
			err = k8sClient.Get(ctx, types.NamespacedName{Name: "argocd"}, argocdNamespace)
			if err != nil && errors.IsNotFound(err) {
				Expect(k8sClient.Create(ctx, argocdNamespace)).To(Succeed())
			}
		})

		AfterEach(func() {
			resource := &previewv1alpha1.PreviewEnvironment{}
			err := k8sClient.Get(ctx, typeNamespacedName, resource)
			if err == nil {
				By("Cleanup the specific resource instance PreviewEnvironment")
				Expect(k8sClient.Delete(ctx, resource)).To(Succeed())
			}

			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "preview-pr-142",
				},
			}
			_ = k8sClient.Delete(ctx, ns)
		})

		It("should successfully reconcile the resource through provisioning, health evaluating, ready, degraded, and revision change reset", func() {
			By("Reconciling the created resource (First loop - phase transitions to Provisioning)")
			mockMetrics := &MockMetricsQuerier{
				P99Latency:    150.0,
				ErrorRate:     0.2,
				Restarts:      0,
				TotalRequests: 100.0,
			}
			mockGH := &MockGitHubClient{}

			controllerReconciler := &PreviewEnvironmentReconciler{
				Client:         k8sClient,
				Scheme:         k8sClient.Scheme(),
				GHClient:       mockGH,
				MetricsQuerier: mockMetrics,
			}

			result, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.Requeue).To(BeTrue())

			// Check status is Provisioning
			err = k8sClient.Get(ctx, typeNamespacedName, previewenvironment)
			Expect(err).NotTo(HaveOccurred())
			Expect(previewenvironment.Status.Phase).To(Equal("Provisioning"))

			By("Reconciling the resource again (Second loop - resource creation -> HealthEvaluating)")
			result, err = controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.Requeue).To(BeTrue())

			// Check status is HealthEvaluating
			err = k8sClient.Get(ctx, typeNamespacedName, previewenvironment)
			Expect(err).NotTo(HaveOccurred())
			Expect(previewenvironment.Status.Phase).To(Equal("HealthEvaluating"))

			// Check Namespace exists
			var ns corev1.Namespace
			err = k8sClient.Get(ctx, types.NamespacedName{Name: "preview-pr-142"}, &ns)
			Expect(err).NotTo(HaveOccurred())

			// Check Ingress exists
			var ingress networkingv1.Ingress
			err = k8sClient.Get(ctx, types.NamespacedName{Name: "pr-142-ingress", Namespace: "preview-pr-142"}, &ingress)
			Expect(err).NotTo(HaveOccurred())
			Expect(ingress.Spec.Rules[0].Host).To(Equal("pr-142.preview.company.com"))

			// Check ArgoCD Application exists
			var app unstructured.Unstructured
			app.SetGroupVersionKind(schema.GroupVersionKind{
				Group:   "argoproj.io",
				Version: "v1alpha1",
				Kind:    "Application",
			})
			err = k8sClient.Get(ctx, types.NamespacedName{Name: "pr-142", Namespace: "argocd"}, &app)
			Expect(err).NotTo(HaveOccurred())

			By("Reconciling the resource again (Third loop - SRE metrics evaluation: Passing -> Ready)")
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			// Check Status was updated to Ready and metrics snapshot set
			err = k8sClient.Get(ctx, typeNamespacedName, previewenvironment)
			Expect(err).NotTo(HaveOccurred())
			Expect(previewenvironment.Status.Phase).To(Equal("Ready"))
			Expect(previewenvironment.Status.PreviewURL).To(Equal("https://pr-142.preview.company.com"))
			Expect(previewenvironment.Status.SREMetrics.CurrentP99LatencyMs).To(Equal(150.0))
			Expect(previewenvironment.Status.SREMetrics.CurrentErrorRatePercent).To(Equal(0.2))
			Expect(previewenvironment.Status.SREMetrics.CurrentPodRestarts).To(Equal(0))

			// Verify GitHub comment was posted for Ready phase
			Expect(mockGH.LastComment).To(ContainSubstring("Preview Environment is Ready!"))
			Expect(mockGH.LastComment).To(ContainSubstring("150.0ms"))

			By("Reconciling the resource again with failing metrics (SRE metrics evaluation: Failing -> Degraded)")
			mockMetrics.P99Latency = 450.0 // Threshold is 300ms
			mockMetrics.ErrorRate = 2.5    // Threshold is 1.0%

			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			// Check Status was updated to Degraded and metrics snapshot set
			err = k8sClient.Get(ctx, typeNamespacedName, previewenvironment)
			Expect(err).NotTo(HaveOccurred())
			Expect(previewenvironment.Status.Phase).To(Equal("Degraded"))
			Expect(previewenvironment.Status.SREMetrics.CurrentP99LatencyMs).To(Equal(450.0))
			Expect(previewenvironment.Status.SREMetrics.CurrentErrorRatePercent).To(Equal(2.5))

			// Verify GitHub comment was posted for Degraded phase
			Expect(mockGH.LastComment).To(ContainSubstring("Preview Environment is Degraded"))
			Expect(mockGH.LastComment).To(ContainSubstring("High P99 Latency"))
			Expect(mockGH.LastComment).To(ContainSubstring("450.0ms"))

			By("Reconciling the resource again with updated GitOps TargetRevision (synchronize/new commit -> Reset to Provisioning)")
			// Get current CR, modify, and update
			err = k8sClient.Get(ctx, typeNamespacedName, previewenvironment)
			Expect(err).NotTo(HaveOccurred())
			previewenvironment.Spec.GitOps.TargetRevision = "feature/payment-gateway-updated"
			err = k8sClient.Update(ctx, previewenvironment)
			Expect(err).NotTo(HaveOccurred())

			result, err = controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.Requeue).To(BeTrue())

			// Verify phase was reset to Provisioning
			err = k8sClient.Get(ctx, typeNamespacedName, previewenvironment)
			Expect(err).NotTo(HaveOccurred())
			Expect(previewenvironment.Status.Phase).To(Equal("Provisioning"))
		})

		It("should successfully hibernate when idle and wake up when requested", func() {
			mockMetrics := &MockMetricsQuerier{
				P99Latency:    100.0,
				ErrorRate:     0.0,
				Restarts:      0,
				TotalRequests: 100.0, // Initial traffic
			}
			mockGH := &MockGitHubClient{}

			controllerReconciler := &PreviewEnvironmentReconciler{
				Client:         k8sClient,
				Scheme:         k8sClient.Scheme(),
				GHClient:       mockGH,
				MetricsQuerier: mockMetrics,
			}

			req := reconcile.Request{NamespacedName: typeNamespacedName}

			// 1. Move CR through Provisioning -> HealthEvaluating -> Ready
			Eventually(func(g Gomega) string {
				_, _ = controllerReconciler.Reconcile(ctx, req)
				var currentEnv previewv1alpha1.PreviewEnvironment
				g.Expect(k8sClient.Get(ctx, typeNamespacedName, &currentEnv)).To(Succeed())
				return currentEnv.Status.Phase
			}, "5s", "100ms").Should(Equal("Ready"))

			// 2. Set TotalRequests to 0 to simulate idle traffic
			mockMetrics.TotalRequests = 0.0

			_, err := controllerReconciler.Reconcile(ctx, req)
			Expect(err).NotTo(HaveOccurred())

			err = k8sClient.Get(ctx, typeNamespacedName, previewenvironment)
			Expect(err).NotTo(HaveOccurred())
			Expect(previewenvironment.Status.Phase).To(Equal("Hibernating"))
			Expect(previewenvironment.Status.Hibernation.IsHibernating).To(BeTrue())
			Expect(mockGH.LastComment).To(ContainSubstring("Preview Environment Hibernated"))

			// 3. Trigger Wakeup by resuming traffic and updating status.hibernation.isHibernating = false
			mockMetrics.TotalRequests = 100.0
			err = k8sClient.Get(ctx, typeNamespacedName, previewenvironment)
			Expect(err).NotTo(HaveOccurred())
			previewenvironment.Status.Hibernation.IsHibernating = false
			Expect(k8sClient.Status().Update(ctx, previewenvironment)).To(Succeed())

			_, err = controllerReconciler.Reconcile(ctx, req)
			Expect(err).NotTo(HaveOccurred())

			Eventually(func(g Gomega) string {
				_, _ = controllerReconciler.Reconcile(ctx, req)
				var currentEnv previewv1alpha1.PreviewEnvironment
				g.Expect(k8sClient.Get(ctx, typeNamespacedName, &currentEnv)).To(Succeed())
				return currentEnv.Status.Phase
			}, "5s", "100ms").Should(Equal("Ready"))
			Expect(previewenvironment.Status.Hibernation.IsHibernating).To(BeFalse())
			Expect(mockGH.LastComment).To(ContainSubstring("Preview Environment Woken Up!"))
		})
	})
})
