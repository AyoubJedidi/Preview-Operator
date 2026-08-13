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

package github

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	previewv1alpha1 "github.com/AyoubJedidi/preview-operator/api/v1alpha1"
)

var log = logf.Log.WithName("github-receiver")

// PullRequestPayload represents the subset of GitHub's Pull Request payload fields we need.
type PullRequestPayload struct {
	Action      string          `json:"action"`
	Number      int             `json:"number"`
	PullRequest PullRequestInfo `json:"pull_request"`
	Repository  RepositoryInfo  `json:"repository"`
}

type PullRequestInfo struct {
	Number int      `json:"number"`
	State  string   `json:"state"`
	Head   HeadInfo `json:"head"`
}

type HeadInfo struct {
	Ref  string         `json:"ref"`
	Sha  string         `json:"sha"`
	Repo RepositoryInfo `json:"repo"`
}

type RepositoryInfo struct {
	Name  string    `json:"name"`
	Owner OwnerInfo `json:"owner"`
}

type OwnerInfo struct {
	Login string `json:"login"`
}

// WebhookReceiver implements manager.Runnable and runs an HTTP server to receive GitHub webhooks.
type WebhookReceiver struct {
	Client        client.Client
	Scheme        *runtime.Scheme
	BindAddress   string
	WebhookSecret string
	GHClient      *Client
	Domain        string
	GitopsPath    string
	Namespace     string
}

// Start starts the webhook HTTP server and blocks until the context is cancelled.
func (w *WebhookReceiver) Start(ctx context.Context) error {
	log.Info("Starting GitHub Webhook Receiver", "bindAddress", w.BindAddress)

	mux := http.NewServeMux()
	mux.HandleFunc("/webhook", w.handleWebhook)

	server := &http.Server{
		Addr:    w.BindAddress,
		Handler: mux,
	}

	// Start server in goroutine
	errChan := make(chan error, 1)
	go func() {
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error(err, "HTTP server failed")
			errChan <- err
		}
	}()

	// Block until context is done or server fails
	select {
	case <-ctx.Done():
		log.Info("Shutting down GitHub Webhook Receiver server...")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			log.Error(err, "Failed to shutdown HTTP server cleanly")
			return err
		}
		return nil
	case err := <-errChan:
		return err
	}
}

// handleWebhook handles the incoming HTTP POST webhook requests from GitHub.
func (w *WebhookReceiver) handleWebhook(rw http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(rw, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	event := req.Header.Get("X-GitHub-Event")
	if event != "pull_request" {
		rw.WriteHeader(http.StatusOK)
		_, _ = rw.Write([]byte("Event ignored; not a pull_request event"))
		return
	}

	body, err := io.ReadAll(req.Body)
	if err != nil {
		log.Error(err, "Failed to read request body")
		http.Error(rw, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	defer req.Body.Close()

	sigHeader := req.Header.Get("X-Hub-Signature-256")
	if !w.verifySignature(body, sigHeader) {
		log.Info("Signature verification failed")
		http.Error(rw, "Unauthorized", http.StatusUnauthorized)
		return
	}

	var payload PullRequestPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		log.Error(err, "Failed to unmarshal GitHub payload JSON")
		http.Error(rw, "BadRequest", http.StatusBadRequest)
		return
	}

	ctx := req.Context()
	if err := w.processPayload(ctx, payload); err != nil {
		log.Error(err, "Failed to process PR event payload", "action", payload.Action, "prNumber", payload.Number)
		http.Error(rw, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	rw.WriteHeader(http.StatusOK)
	_, _ = rw.Write([]byte("Event processed successfully"))
}

// verifySignature validates the incoming webhook signature with the secret HMAC key.
func (w *WebhookReceiver) verifySignature(body []byte, signatureHeader string) bool {
	if w.WebhookSecret == "" {
		// Secret check bypassed if not configured (useful for tests/local dev)
		return true
	}
	if !strings.HasPrefix(signatureHeader, "sha256=") {
		return false
	}
	hexSig := signatureHeader[len("sha256="):]
	expectedSig := make([]byte, hex.DecodedLen(len(hexSig)))
	_, err := hex.Decode(expectedSig, []byte(hexSig))
	if err != nil {
		return false
	}

	mac := hmac.New(sha256.New, []byte(w.WebhookSecret))
	mac.Write(body)
	actualSig := mac.Sum(nil)

	return hmac.Equal(actualSig, expectedSig)
}

// processPayload reconciles the GitHub PR state with PreviewEnvironment CRD.
func (w *WebhookReceiver) processPayload(ctx context.Context, payload PullRequestPayload) error {
	prName := fmt.Sprintf("pr-%d", payload.Number)
	owner := payload.Repository.Owner.Login
	repo := payload.Repository.Name

	log.Info("Processing PR webhook event", "action", payload.Action, "prNumber", payload.Number, "repo", repo, "owner", owner)

	key := types.NamespacedName{
		Name:      prName,
		Namespace: w.Namespace,
	}

	switch payload.Action {
	case "opened", "reopened", "synchronize":
		var env previewv1alpha1.PreviewEnvironment
		err := w.Client.Get(ctx, key, &env)
		if err != nil {
			if apierrors.IsNotFound(err) {
				newEnv := &previewv1alpha1.PreviewEnvironment{
					ObjectMeta: metav1.ObjectMeta{
						Name:      key.Name,
						Namespace: key.Namespace,
					},
					Spec: previewv1alpha1.PreviewEnvironmentSpec{
						PRNumber:  payload.Number,
						RepoOwner: owner,
						RepoName:  repo,
						Branch:    payload.PullRequest.Head.Ref,
						CommitSha: payload.PullRequest.Head.Sha,
						Domain:    w.Domain,
						GitOps: previewv1alpha1.GitOpsConfig{
							TargetRevision: payload.PullRequest.Head.Ref,
							Path:           w.GitopsPath,
						},
					},
				}
				if err := w.Client.Create(ctx, newEnv); err != nil {
					return fmt.Errorf("failed to create PreviewEnvironment CR: %w", err)
				}
				log.Info("Created PreviewEnvironment CR", "name", key.Name)
			} else {
				return fmt.Errorf("failed to get PreviewEnvironment CR: %w", err)
			}
		} else {
			env.Spec.Branch = payload.PullRequest.Head.Ref
			env.Spec.CommitSha = payload.PullRequest.Head.Sha
			env.Spec.GitOps.TargetRevision = payload.PullRequest.Head.Ref
			if err := w.Client.Update(ctx, &env); err != nil {
				return fmt.Errorf("failed to update PreviewEnvironment CR: %w", err)
			}
			log.Info("Updated PreviewEnvironment CR", "name", key.Name)
		}

		// Comment back to GitHub PR
		comment := fmt.Sprintf("🚀 Preview environment provisioning started for commit `%s`. Custom Resource `%s` created/updated successfully.", payload.PullRequest.Head.Sha, prName)
		if err := w.GHClient.PostPRComment(ctx, owner, repo, payload.Number, comment); err != nil {
			log.Error(err, "Failed to post PR status comment", "prNumber", payload.Number)
		}

	case "closed":
		var env previewv1alpha1.PreviewEnvironment
		err := w.Client.Get(ctx, key, &env)
		if err == nil {
			if err := w.Client.Delete(ctx, &env); err != nil {
				return fmt.Errorf("failed to delete PreviewEnvironment CR: %w", err)
			}
			log.Info("Deleted PreviewEnvironment CR", "name", key.Name)

			// Comment back to GitHub PR
			comment := fmt.Sprintf("🧹 Preview environment teardown triggered for PR #%d. Custom Resource `%s` has been deleted.", payload.Number, prName)
			if err := w.GHClient.PostPRComment(ctx, owner, repo, payload.Number, comment); err != nil {
				log.Error(err, "Failed to post PR teardown comment", "prNumber", payload.Number)
			}
		} else if !apierrors.IsNotFound(err) {
			return fmt.Errorf("failed to check PreviewEnvironment CR before deletion: %w", err)
		}
	}

	return nil
}
