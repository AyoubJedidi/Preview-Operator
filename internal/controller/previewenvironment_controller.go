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
	"time"

	appsv1 "k8s.io/api/apps/v1"
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

// GitHubClient defines the interface for posting PR comments.
type GitHubClient interface {
	PostPRComment(ctx context.Context, owner, repo string, prNumber int, comment string) error
}

// PreviewEnvironmentReconciler reconciles a PreviewEnvironment object
type PreviewEnvironmentReconciler struct {
	client.Client
	Scheme         *runtime.Scheme
	GHClient       GitHubClient
	MetricsQuerier MetricsQuerier
}

// +kubebuilder:rbac:groups=preview.preview.io,resources=previewenvironments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=preview.preview.io,resources=previewenvironments/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=preview.preview.io,resources=previewenvironments/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=networking.k8s.io,resources=ingresses,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=argoproj.io,resources=applications,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apps,resources=deployments;statefulsets,verbs=get;list;watch;create;update;patch;delete

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
		var revisionChanged bool
		if existingSpec, ok := existingApp.Object["spec"].(map[string]interface{}); ok {
			if source, ok := existingSpec["source"].(map[string]interface{}); ok {
				if existingRevision, ok := source["targetRevision"].(string); ok {
					if existingRevision != env.Spec.GitOps.TargetRevision {
						revisionChanged = true
					}
				}
			}
		}

		existingApp.Object["spec"] = appSpec
		if err := r.Update(ctx, &existingApp); err != nil {
			log.Error(err, "Failed to update ArgoCD Application", "name", appName, "namespace", argoNamespace)
			return ctrl.Result{}, err
		}

		if revisionChanged {
			log.Info("Detected new revision, resetting phase to Provisioning", "name", appName, "targetRevision", env.Spec.GitOps.TargetRevision)
			env.Status.Phase = "Provisioning"
			env.Status.ArgoAppStatus = "Progressing"
			if err := r.Status().Update(ctx, &env); err != nil {
				log.Error(err, "Failed to reset phase to Provisioning on new commit")
				return ctrl.Result{}, err
			}
			return ctrl.Result{Requeue: true}, nil
		}
	}

	// 6. Update Status
	if env.Status.Phase == "Provisioning" {
		var latestApp unstructured.Unstructured
		latestApp.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   "argoproj.io",
			Version: "v1alpha1",
			Kind:    "Application",
		})
		err = r.Get(ctx, types.NamespacedName{Name: appName, Namespace: argoNamespace}, &latestApp)
		if err != nil {
			log.Error(err, "Failed to fetch latest ArgoCD Application status")
			return ctrl.Result{}, err
		}

		syncStatus, _, _ := unstructured.NestedString(latestApp.Object, "status", "sync", "status")
		healthStatus, _, _ := unstructured.NestedString(latestApp.Object, "status", "health", "status")

		if syncStatus == "" {
			syncStatus = "Synced"
		}
		if healthStatus == "" {
			healthStatus = "Healthy"
		}

		env.Status.ArgoAppStatus = healthStatus

		if syncStatus == "Synced" && healthStatus == "Healthy" {
			env.Status.Phase = "HealthEvaluating"
			if err := r.Status().Update(ctx, &env); err != nil {
				log.Error(err, "Failed to update phase to HealthEvaluating")
				return ctrl.Result{}, err
			}
			log.Info("ArgoCD Application is synced and healthy, transitioning to HealthEvaluating", "name", env.Name)
			return ctrl.Result{Requeue: true}, nil
		} else {
			if err := r.Status().Update(ctx, &env); err != nil {
				log.Error(err, "Failed to update ArgoAppStatus in status")
				return ctrl.Result{}, err
			}
			log.Info("Waiting for ArgoCD Application to become Synced and Healthy", "name", env.Name, "sync", syncStatus, "health", healthStatus)
			return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
		}
	}

	// 6. FinOps Hibernating Check (Top-Level)
	if env.Status.Phase == "Hibernating" || env.Status.Hibernation.IsHibernating {
		// If wake-up triggered (manually setting isHibernating: false while phase is Hibernating)
		if !env.Status.Hibernation.IsHibernating && env.Status.Phase == "Hibernating" {
			log.Info("Wakeup triggered for hibernated environment", "name", env.Name)
			if err := r.wakeEnvironment(ctx, targetNamespace, &env); err != nil {
				log.Error(err, "Failed to wake up environment")
				return ctrl.Result{}, err
			}
			return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
		}
		// Maintain hibernating state
		return ctrl.Result{RequeueAfter: 1 * time.Minute}, nil
	}

	if env.Status.Phase == "HealthEvaluating" || env.Status.Phase == "Ready" || env.Status.Phase == "Degraded" {
		if env.Status.PreviewURL == "" {
			env.Status.PreviewURL = fmt.Sprintf("https://%s", hostName)
		}

		policy := getHealthPolicy(&env)
		var latency float64
		var errorRate float64
		var restarts int
		var queryErr error

		if r.MetricsQuerier != nil {
			latency, queryErr = r.MetricsQuerier.QueryP99Latency(ctx, targetNamespace, policy.EvaluationWindow)
			if queryErr == nil {
				errorRate, queryErr = r.MetricsQuerier.QueryErrorRate(ctx, targetNamespace, policy.EvaluationWindow)
			}
			if queryErr == nil {
				restarts, queryErr = r.MetricsQuerier.QueryPodRestarts(ctx, targetNamespace)
			}
		}

		if queryErr != nil {
			log.Error(queryErr, "Failed to query SRE metrics from Prometheus")
			return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
		}

		latencyPassed := latency <= float64(policy.MaxP99LatencyMs)
		errorRatePassed := errorRate <= policy.MaxErrorRatePercent
		restartsPassed := restarts <= policy.MaxPodRestarts

		isHealthy := latencyPassed && errorRatePassed && restartsPassed

		oldPhase := env.Status.Phase
		var newPhase string
		if isHealthy {
			newPhase = "Ready"
		} else {
			newPhase = "Degraded"
		}

		env.Status.SREMetrics = previewv1alpha1.SREMetricsSnapshot{
			CurrentErrorRatePercent: errorRate,
			CurrentP99LatencyMs:     latency,
			CurrentPodRestarts:      restarts,
			LastEvaluatedAt:         time.Now().Format(time.RFC3339),
		}

		phaseChanged := oldPhase != newPhase

		if phaseChanged {
			env.Status.Phase = newPhase
			if err := r.Status().Update(ctx, &env); err != nil {
				log.Error(err, "Failed to update PreviewEnvironment Phase & Metrics")
				return ctrl.Result{}, err
			}
			log.Info("Phase transitioned", "old", oldPhase, "new", newPhase, "name", env.Name)

			if r.GHClient != nil {
				var comment string
				if isHealthy {
					comment = fmt.Sprintf("### 🚀 Preview Environment is Ready!\n\n"+
						"**URL**: [%s](%s)\n"+
						"**Status**: Green/Healthy\n\n"+
						"#### 📊 SRE Golden Signals Health Gating Snapshot\n"+
						"- **P99 Latency**: `%.1fms` (Threshold: `<%dms`)\n"+
						"- **HTTP 5xx Error Rate**: `%.2f%%` (Threshold: `<%.2f%%`)\n"+
						"- **Container Restarts**: `%d` (Threshold: `%d`)",
						env.Status.PreviewURL, env.Status.PreviewURL,
						latency, policy.MaxP99LatencyMs,
						errorRate, policy.MaxErrorRatePercent,
						restarts, policy.MaxPodRestarts)
				} else {
					var violations []string
					if !latencyPassed {
						violations = append(violations, fmt.Sprintf("High P99 Latency: `%.1fms` (threshold: `%dms`)", latency, policy.MaxP99LatencyMs))
					}
					if !errorRatePassed {
						violations = append(violations, fmt.Sprintf("High HTTP 5xx Error Rate: `%.2f%%` (threshold: `%.2f%%`)", errorRate, policy.MaxErrorRatePercent))
					}
					if !restartsPassed {
						violations = append(violations, fmt.Sprintf("High Container Restarts: `%d` (threshold: `%d`)", restarts, policy.MaxPodRestarts))
					}

					comment = fmt.Sprintf("### ⚠️ Preview Environment is Degraded\n\n"+
						"**URL**: [%s](%s)\n"+
						"**Status**: Red/Degraded\n\n"+
						"#### ❌ Violations:\n- %s\n\n"+
						"#### 📊 SRE Golden Signals Health Gating Snapshot\n"+
						"- **P99 Latency**: `%.1fms` %s\n"+
						"- **HTTP 5xx Error Rate**: `%.2f%%` %s\n"+
						"- **Container Restarts**: `%d` %s",
						env.Status.PreviewURL, env.Status.PreviewURL,
						strings.Join(violations, "\n- "),
						latency, checkMark(!latencyPassed),
						errorRate, checkMark(!errorRatePassed),
						restarts, checkMark(!restartsPassed))
				}

				if err := r.GHClient.PostPRComment(ctx, env.Spec.RepoOwner, env.Spec.RepoName, env.Spec.PRNumber, comment); err != nil {
					log.Error(err, "Failed to post SRE metrics status comment to GitHub")
				} else {
					log.Info("Successfully posted SRE metrics comment to GitHub", "pr", env.Spec.PRNumber)
				}
			}
		} else {
			if err := r.Status().Update(ctx, &env); err != nil {
				log.Error(err, "Failed to update SRE metrics snapshot in status")
				return ctrl.Result{}, err
			}
		}

		// 7. FinOps Auto-Hibernation Trigger
		if (env.Status.Phase == "Ready" || env.Status.Phase == "Degraded") && env.Spec.FinOps.AutoHibernate {
			shouldHibernate := env.Status.Hibernation.IsHibernating
			if !shouldHibernate && r.MetricsQuerier != nil {
				idleWindow := env.Spec.FinOps.IdleDuration
				if idleWindow == "" {
					idleWindow = "2h"
				}
				totalReqs, err := r.MetricsQuerier.QueryTotalRequests(ctx, targetNamespace, idleWindow)
				if err == nil && totalReqs == 0 {
					log.Info("No traffic detected over idle duration, triggering auto-hibernation", "name", env.Name, "idleDuration", idleWindow)
					shouldHibernate = true
				}
			}

			if shouldHibernate {
				if err := r.hibernateEnvironment(ctx, targetNamespace, &env); err != nil {
					log.Error(err, "Failed to hibernate environment")
					return ctrl.Result{}, err
				}
				return ctrl.Result{RequeueAfter: 1 * time.Minute}, nil
			}
		}

		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	return ctrl.Result{}, nil
}

func (r *PreviewEnvironmentReconciler) hibernateEnvironment(ctx context.Context, namespace string, env *previewv1alpha1.PreviewEnvironment) error {
	log := logf.FromContext(ctx)

	savedReplicas := make(map[string]int32)

	// 1. Scale Deployments
	var depList appsv1.DeploymentList
	if err := r.List(ctx, &depList, client.InNamespace(namespace)); err == nil {
		for i := range depList.Items {
			dep := &depList.Items[i]
			if dep.Spec.Replicas != nil && *dep.Spec.Replicas > 0 {
				savedReplicas[dep.Name] = *dep.Spec.Replicas
				dep.Spec.Replicas = ptrInt32(0)
				if err := r.Update(ctx, dep); err != nil {
					log.Error(err, "Failed to scale down Deployment for hibernation", "name", dep.Name)
				}
			}
		}
	}

	// 2. Scale StatefulSets
	var stsList appsv1.StatefulSetList
	if err := r.List(ctx, &stsList, client.InNamespace(namespace)); err == nil {
		for i := range stsList.Items {
			sts := &stsList.Items[i]
			if sts.Spec.Replicas != nil && *sts.Spec.Replicas > 0 {
				savedReplicas[sts.Name] = *sts.Spec.Replicas
				sts.Spec.Replicas = ptrInt32(0)
				if err := r.Update(ctx, sts); err != nil {
					log.Error(err, "Failed to scale down StatefulSet for hibernation", "name", sts.Name)
				}
			}
		}
	}

	var latestEnv previewv1alpha1.PreviewEnvironment
	if err := r.Get(ctx, types.NamespacedName{Name: env.Name, Namespace: env.Namespace}, &latestEnv); err != nil {
		return fmt.Errorf("failed to re-fetch PreviewEnvironment before status update: %w", err)
	}

	latestEnv.Status.Phase = "Hibernating"
	latestEnv.Status.Hibernation = previewv1alpha1.HibernationState{
		IsHibernating: true,
		SavedReplicas: savedReplicas,
		LastActiveAt:  time.Now().Format(time.RFC3339),
	}

	if err := r.Status().Update(ctx, &latestEnv); err != nil {
		return fmt.Errorf("failed to update status to Hibernating: %w", err)
	}
	*env = latestEnv

	log.Info("Successfully hibernated environment", "name", env.Name, "namespace", namespace)

	if r.GHClient != nil {
		comment := fmt.Sprintf("### 💤 Preview Environment Hibernated\n\n"+
			"Workloads in namespace `%s` have been scaled to **0 replicas** to save cloud compute costs due to inactivity.\n\n"+
			"- **Idle Duration**: `%s`\n"+
			"- **Status**: Hibernating 💤\n"+
			"- **Saved Workloads**: `%d`",
			namespace, env.Spec.FinOps.IdleDuration, len(savedReplicas))
		_ = r.GHClient.PostPRComment(ctx, env.Spec.RepoOwner, env.Spec.RepoName, env.Spec.PRNumber, comment)
	}

	return nil
}

func (r *PreviewEnvironmentReconciler) wakeEnvironment(ctx context.Context, namespace string, env *previewv1alpha1.PreviewEnvironment) error {
	log := logf.FromContext(ctx)

	savedMap := env.Status.Hibernation.SavedReplicas
	if savedMap == nil {
		savedMap = make(map[string]int32)
	}

	// 1. Restore Deployments
	var depList appsv1.DeploymentList
	if err := r.List(ctx, &depList, client.InNamespace(namespace)); err == nil {
		for i := range depList.Items {
			dep := &depList.Items[i]
			targetReplicas := int32(1)
			if original, exists := savedMap[dep.Name]; exists && original > 0 {
				targetReplicas = original
			}
			dep.Spec.Replicas = ptrInt32(targetReplicas)
			if err := r.Update(ctx, dep); err != nil {
				log.Error(err, "Failed to restore Deployment replicas", "name", dep.Name)
			}
		}
	}

	// 2. Restore StatefulSets
	var stsList appsv1.StatefulSetList
	if err := r.List(ctx, &stsList, client.InNamespace(namespace)); err == nil {
		for i := range stsList.Items {
			sts := &stsList.Items[i]
			targetReplicas := int32(1)
			if original, exists := savedMap[sts.Name]; exists && original > 0 {
				targetReplicas = original
			}
			sts.Spec.Replicas = ptrInt32(targetReplicas)
			if err := r.Update(ctx, sts); err != nil {
				log.Error(err, "Failed to restore StatefulSet replicas", "name", sts.Name)
			}
		}
	}

	var latestEnv previewv1alpha1.PreviewEnvironment
	if err := r.Get(ctx, types.NamespacedName{Name: env.Name, Namespace: env.Namespace}, &latestEnv); err != nil {
		return fmt.Errorf("failed to re-fetch PreviewEnvironment before status update: %w", err)
	}

	latestEnv.Status.Phase = "Ready"
	latestEnv.Status.Hibernation.IsHibernating = false

	if err := r.Status().Update(ctx, &latestEnv); err != nil {
		return fmt.Errorf("failed to update status to Ready after wakeup: %w", err)
	}
	*env = latestEnv

	log.Info("Successfully woken up environment", "name", env.Name, "namespace", namespace)

	if r.GHClient != nil {
		comment := fmt.Sprintf("### ⚡ Preview Environment Woken Up!\n\n"+
			"The preview workloads have been restored and scaled back to active replicas.\n\n"+
			"- **URL**: [%s](%s)\n"+
			"- **Status**: Active ⚡",
			env.Status.PreviewURL, env.Status.PreviewURL)
		_ = r.GHClient.PostPRComment(ctx, env.Spec.RepoOwner, env.Spec.RepoName, env.Spec.PRNumber, comment)
	}

	return nil
}

func ptrInt32(v int32) *int32 {
	return &v
}

func getHealthPolicy(env *previewv1alpha1.PreviewEnvironment) previewv1alpha1.HealthPolicyConfig {
	policy := env.Spec.HealthPolicy
	if policy.EvaluationWindow == "" {
		policy.EvaluationWindow = "3m"
	}
	if policy.MaxErrorRatePercent == 0 {
		policy.MaxErrorRatePercent = 1.0
	}
	if policy.MaxP99LatencyMs == 0 {
		policy.MaxP99LatencyMs = 300
	}
	return policy
}

func checkMark(failed bool) string {
	if failed {
		return "❌"
	}
	return "✅"
}

// SetupWithManager sets up the controller with the Manager.
func (r *PreviewEnvironmentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&previewv1alpha1.PreviewEnvironment{}).
		Named("previewenvironment").
		Complete(r)
}
