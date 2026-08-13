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
	"os"
	"strings"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	previewv1alpha1 "github.com/AyoubJedidi/preview-operator/api/v1alpha1"
)

// PreviewEnvironmentReconciler reconciles a PreviewEnvironment object
type PreviewEnvironmentReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=preview.preview.io,resources=previewenvironments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=preview.preview.io,resources=previewenvironments/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=preview.preview.io,resources=previewenvironments/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=networking.k8s.io,resources=ingresses,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=argoproj.io,resources=applications,verbs=get;list;watch;create;update;patch;delete

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *PreviewEnvironmentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// 1. Fetch the PreviewEnvironment resource
	var env previewv1alpha1.PreviewEnvironment
	if err := r.Get(ctx, req.NamespacedName, &env); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		log.Error(err, "Failed to get PreviewEnvironment")
		return ctrl.Result{}, err
	}

	// 2. Set phase to Provisioning if it's Pending/new
	if env.Status.Phase == "" || env.Status.Phase == "Pending" {
		env.Status.Phase = "Provisioning"
		if err := r.Status().Update(ctx, &env); err != nil {
			log.Error(err, "Failed to update PreviewEnvironment phase to Provisioning")
			return ctrl.Result{}, err
		}
		log.Info("Phase updated to Provisioning", "name", env.Name)
		return ctrl.Result{Requeue: true}, nil
	}

	targetNamespace := fmt.Sprintf("preview-pr-%d", env.Spec.PRNumber)

	// 3. Reconcile Namespace
	var ns corev1.Namespace
	err := r.Get(ctx, types.NamespacedName{Name: targetNamespace}, &ns)
	if err != nil {
		if apierrors.IsNotFound(err) {
			ns = corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: targetNamespace,
					Labels: map[string]string{
						"app.kubernetes.io/managed-by": "preview-operator",
					},
				},
			}
			if err := r.Create(ctx, &ns); err != nil {
				log.Error(err, "Failed to create target Namespace", "namespace", targetNamespace)
				return ctrl.Result{}, err
			}
			log.Info("Created target Namespace", "namespace", targetNamespace)
		} else {
			log.Error(err, "Failed to check target Namespace", "namespace", targetNamespace)
			return ctrl.Result{}, err
		}
	}

	// 4. Reconcile Ingress
	ingressName := fmt.Sprintf("pr-%d-ingress", env.Spec.PRNumber)
	hostName := fmt.Sprintf("pr-%d.%s", env.Spec.PRNumber, env.Spec.Domain)
	secretName := fmt.Sprintf("pr-%d-tls", env.Spec.PRNumber)

	var ingress networkingv1.Ingress
	err = r.Get(ctx, types.NamespacedName{Name: ingressName, Namespace: targetNamespace}, &ingress)

	pathType := networkingv1.PathTypePrefix
	ingressSpec := networkingv1.IngressSpec{
		TLS: []networkingv1.IngressTLS{
			{
				Hosts:      []string{hostName},
				SecretName: secretName,
			},
		},
		Rules: []networkingv1.IngressRule{
			{
				Host: hostName,
				IngressRuleValue: networkingv1.IngressRuleValue{
					HTTP: &networkingv1.HTTPIngressRuleValue{
						Paths: []networkingv1.HTTPIngressPath{
							{
								Path:     "/",
								PathType: &pathType,
								Backend: networkingv1.IngressBackend{
									Service: &networkingv1.IngressServiceBackend{
										Name: env.Spec.RepoName,
										Port: networkingv1.ServiceBackendPort{
											Number: 80,
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}

	ingressAnnotations := map[string]string{
		"kubernetes.io/ingress.class":              "nginx",
		"cert-manager.io/cluster-issuer":           "letsencrypt-prod",
		"nginx.ingress.kubernetes.io/ssl-redirect": "true",
	}

	if err != nil {
		if apierrors.IsNotFound(err) {
			ingress = networkingv1.Ingress{
				ObjectMeta: metav1.ObjectMeta{
					Name:        ingressName,
					Namespace:   targetNamespace,
					Annotations: ingressAnnotations,
				},
				Spec: ingressSpec,
			}
			if err := r.Create(ctx, &ingress); err != nil {
				log.Error(err, "Failed to create Ingress", "name", ingressName, "namespace", targetNamespace)
				return ctrl.Result{}, err
			}
			log.Info("Created Ingress", "name", ingressName, "namespace", targetNamespace)
		} else {
			log.Error(err, "Failed to check Ingress", "name", ingressName, "namespace", targetNamespace)
			return ctrl.Result{}, err
		}
	} else {
		ingress.Spec = ingressSpec
		if ingress.Annotations == nil {
			ingress.Annotations = map[string]string{}
		}
		for k, v := range ingressAnnotations {
			ingress.Annotations[k] = v
		}
		if err := r.Update(ctx, &ingress); err != nil {
			log.Error(err, "Failed to update Ingress", "name", ingressName, "namespace", targetNamespace)
			return ctrl.Result{}, err
		}
	}

	// 5. Reconcile ArgoCD Application using Unstructured
	argoNamespace := os.Getenv("ARGOCD_NAMESPACE")
	if argoNamespace == "" {
		argoNamespace = "argocd"
	}
	appName := fmt.Sprintf("pr-%d", env.Spec.PRNumber)
	repoURL := fmt.Sprintf("https://github.com/%s/%s.git", env.Spec.RepoOwner, env.Spec.RepoName)

	var helmValuesStr strings.Builder
	for k, v := range env.Spec.GitOps.HelmValues {
		helmValuesStr.WriteString(fmt.Sprintf("%s: %q\n", k, v))
	}

	app := &unstructured.Unstructured{}
	app.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "argoproj.io",
		Version: "v1alpha1",
		Kind:    "Application",
	})
	app.SetName(appName)
	app.SetNamespace(argoNamespace)

	appSpec := map[string]interface{}{
		"project": "default",
		"source": map[string]interface{}{
			"repoURL":        repoURL,
			"targetRevision": env.Spec.GitOps.TargetRevision,
			"path":           env.Spec.GitOps.Path,
		},
		"destination": map[string]interface{}{
			"server":    "https://kubernetes.default.svc",
			"namespace": targetNamespace,
		},
		"syncPolicy": map[string]interface{}{
			"automated": map[string]interface{}{
				"prune":    true,
				"selfHeal": true,
			},
			"syncOptions": []interface{}{
				"CreateNamespace=true",
			},
		},
	}

	if helmValuesStr.Len() > 0 {
		appSpec["source"].(map[string]interface{})["helm"] = map[string]interface{}{
			"values": helmValuesStr.String(),
		}
	}
	app.Object["spec"] = appSpec

	var existingApp unstructured.Unstructured
	existingApp.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "argoproj.io",
		Version: "v1alpha1",
		Kind:    "Application",
	})
	err = r.Get(ctx, types.NamespacedName{Name: appName, Namespace: argoNamespace}, &existingApp)
	if err != nil {
		if apierrors.IsNotFound(err) {
			if err := r.Create(ctx, app); err != nil {
				log.Error(err, "Failed to create ArgoCD Application", "name", appName, "namespace", argoNamespace)
				return ctrl.Result{}, err
			}
			log.Info("Created ArgoCD Application", "name", appName, "namespace", argoNamespace)
		} else {
			log.Error(err, "Failed to check ArgoCD Application", "name", appName, "namespace", argoNamespace)
			return ctrl.Result{}, err
		}
	} else {
		existingApp.Object["spec"] = appSpec
		if err := r.Update(ctx, &existingApp); err != nil {
			log.Error(err, "Failed to update ArgoCD Application", "name", appName, "namespace", argoNamespace)
			return ctrl.Result{}, err
		}
	}

	// 6. Update Status
	if env.Status.Phase != "Ready" || env.Status.PreviewURL == "" || env.Status.ArgoAppStatus != "Healthy" {
		env.Status.Phase = "Ready"
		env.Status.PreviewURL = fmt.Sprintf("https://%s", hostName)
		env.Status.ArgoAppStatus = "Healthy"
		if err := r.Status().Update(ctx, &env); err != nil {
			log.Error(err, "Failed to update PreviewEnvironment Status")
			return ctrl.Result{}, err
		}
		log.Info("Reconciliation successful; Status updated to Ready", "name", env.Name)
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *PreviewEnvironmentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&previewv1alpha1.PreviewEnvironment{}).
		Named("previewenvironment").
		Complete(r)
}
