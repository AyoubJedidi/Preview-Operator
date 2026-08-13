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

		It("should successfully reconcile the resource", func() {
			By("Reconciling the created resource (First loop - phase transitions to Provisioning)")
			controllerReconciler := &PreviewEnvironmentReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
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

			By("Reconciling the resource again (Second loop - resource creation)")
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

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

			// Check Status was updated to Ready
			err = k8sClient.Get(ctx, typeNamespacedName, previewenvironment)
			Expect(err).NotTo(HaveOccurred())
			Expect(previewenvironment.Status.Phase).To(Equal("Ready"))
			Expect(previewenvironment.Status.PreviewURL).To(Equal("https://pr-142.preview.company.com"))
			Expect(previewenvironment.Status.ArgoAppStatus).To(Equal("Healthy"))
		})
	})
})
