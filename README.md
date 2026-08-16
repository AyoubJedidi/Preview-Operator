# PreviewOperator — Kubernetes Operator for GitOps PR Preview Environments

[![Go Reference](https://img.shields.io/badge/Go-1.22+-00ADD8?style=flat-square&logo=go)](https://golang.org)
[![Kubernetes](https://img.shields.io/badge/Kubernetes-1.28+-326CE5?style=flat-square&logo=kubernetes)](https://kubernetes.io)
[![ArgoCD](https://img.shields.io/badge/GitOps-ArgoCD-EF7B4D?style=flat-square&logo=argo)](https://argoproj.github.io/cd)
[![Prometheus](https://img.shields.io/badge/Observability-Prometheus-E6522C?style=flat-square&logo=prometheus)](https://prometheus.io)
[![License](https://img.shields.io/badge/License-Apache_2.0-blue.svg?style=flat-square)](LICENSE)

**PreviewOperator** is a Go-based Kubernetes Operator built with `controller-runtime` and `kubebuilder` that automates the complete lifecycle of Pull-Request (PR) preview environments. It integrates GitHub Webhooks, **ArgoCD GitOps Applications**, **Prometheus SRE Golden Signals Health Gating**, **FinOps Auto-Hibernation**, and **Kubernetes Finalizers** for clean teardown.

---

## 🏗️ System Architecture & Workflow

```mermaid
sequenceDiagram
    autonumber
    actor Developer
    participant GitHub as GitHub Webhook API
    participant Op as PreviewOperator (Go)
    participant Argo as ArgoCD API
    participant Prom as Prometheus SRE
    participant K8s as Kubernetes Cluster

    Developer->>GitHub: Open PR #142
    GitHub->>Op: POST /webhook (pull_request.opened)
    Op->>K8s: Create PreviewEnvironment CR (pr-142)
    Op->>Argo: Create ArgoCD Application (pr-142)
    Argo->>K8s: Sync Manifests (Namespace, Deployment, Ingress, Certs)
    
    loop SRE Health Gating
        Op->>Prom: Query P99 Latency, 5xx Error Rate, Pod Restarts
        Prom-->>Op: Return SRE Golden Signals
    end
    
    alt Signals Pass Thresholds
        Op->>K8s: Update CR Status -> Ready
        Op->>GitHub: Comment: "Preview Ready: https://pr-142.preview.company.com (Healthy)"
    else Signals Fail
        Op->>K8s: Update CR Status -> Degraded
        Op->>GitHub: Comment: "Preview Degraded: High 5xx Error Rate (3.2%)"
    end

    Note over Op,K8s: FinOps Auto-Hibernation Loop
    Op->>Prom: Check Traffic (requests < 5 in idle window)
    Op->>K8s: Scale Replicas -> 0 (Store saved counts in CR Status)

    Developer->>GitHub: Close / Merge PR #142
    GitHub->>Op: POST /webhook (pull_request.closed)
    Op->>Argo: Delete ArgoCD Application (pr-142)
    Op->>K8s: Delete Namespace, Ingress, Certs & PVCs via Finalizer
    Op->>GitHub: Comment: "Preview Environment Cleanup Complete"
```

---

## 🔥 Key Features

- ⚡ **Automated GitOps Provisioning**: Automatically provisions isolated namespaces (`preview-pr-142`) and ArgoCD `Application` specs on PR creation or update.
- 🩺 **Prometheus SRE Health Gating**: Queries real-time PromQL golden signals (P99 latency, HTTP 5xx error rate, pod restarts) before marking an environment `Ready`. Prevents false-positive readiness.
- 💰 **FinOps Auto-Hibernation**: Detects idle environments (>2 hours zero HTTP traffic via PromQL) and automatically scales deployments and statefulsets to `0` replicas, saving up to **65% compute costs**. Wakes up automatically when traffic resumes.
- 🧹 **Automated Lifecycle Teardown**: Uses Kubernetes Finalizers (`preview.preview.io/finalizer`) to orchestrate zero-downtime, cascading resource removal (ArgoCD applications, namespaces, ingress, TLS certificates, PVCs) on PR close/merge.
- 💬 **Developer Feedback Loop**: Posts live, rich markdown comments directly to GitHub PRs with status updates, preview URLs, metrics snapshots, and hibernation notifications.

---

## 📋 Custom Resource Definition (`PreviewEnvironment`)

```yaml
apiVersion: preview.preview.io/v1alpha1
kind: PreviewEnvironment
metadata:
  name: pr-142
  namespace: preview-operator-system
spec:
  prNumber: 142
  repoOwner: AyoubJedidi
  repoName: my-app
  branch: feature/payment-gateway
  commitSha: 7a8b9c0
  domain: preview.company.com
  
  gitops:
    targetRevision: feature/payment-gateway
    path: k8s/overlays/preview
    helmValues:
      env: preview
      pr: "142"

  healthPolicy:
    evaluationWindow: "3m"
    maxErrorRatePercent: 1.0       # Max 1.0% HTTP 5xx errors
    maxP99LatencyMs: 300          # Max 300ms P99 latency
    maxPodRestarts: 0             # Zero crash loops allowed

  finops:
    autoHibernate: true
    idleDuration: "2h"            # Scale to 0 if idle for 2 hours

status:
  phase: Ready                    # Provisioning | HealthEvaluating | Ready | Degraded | Hibernating
  previewUrl: "https://pr-142.preview.company.com"
  argoAppStatus: Healthy
```

---

## 🚀 Installation & Deployment

### Option 1: Install via Kustomize (YAML Bundle)

```bash
# Apply bundled release manifests directly
kubectl apply -f dist/install.yaml
```

### Option 2: Install via Helm Chart

```bash
# Install PreviewOperator using the Helm Chart
helm install preview-operator charts/preview-operator --namespace preview-operator-system --create-namespace
```

---

## 🧪 Development & Testing

### Running Unit Tests

Unit tests execute using `controller-runtime/pkg/envtest` with a localized Kubernetes API server and etcd instance:

```bash
make test
```

### Running E2E Integration Tests

E2E tests deploy the built manager image into an isolated **Kind** cluster and assert end-to-end reconciliation workflows:

```bash
make test-e2e
```

---


---

