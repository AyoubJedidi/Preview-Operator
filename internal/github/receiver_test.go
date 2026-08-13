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
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	previewv1alpha1 "github.com/AyoubJedidi/preview-operator/api/v1alpha1"
)

func TestVerifySignature(t *testing.T) {
	secret := "test-secret"
	body := []byte(`{"action":"opened"}`)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	sig := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	receiver := &WebhookReceiver{WebhookSecret: secret}

	t.Run("Valid signature", func(t *testing.T) {
		if !receiver.verifySignature(body, sig) {
			t.Error("expected signature validation to succeed")
		}
	})

	t.Run("Invalid signature", func(t *testing.T) {
		if receiver.verifySignature(body, "sha256=invalidhash") {
			t.Error("expected signature validation to fail")
		}
	})

	t.Run("No signature prefix", func(t *testing.T) {
		if receiver.verifySignature(body, hex.EncodeToString(mac.Sum(nil))) {
			t.Error("expected signature validation to fail due to missing prefix")
		}
	})

	t.Run("Empty secret bypasses validation", func(t *testing.T) {
		emptyReceiver := &WebhookReceiver{WebhookSecret: ""}
		if !emptyReceiver.verifySignature(body, "invalid") {
			t.Error("expected signature validation to succeed when no secret is configured")
		}
	})
}

func TestWebhookReceiver_HandleWebhook(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = previewv1alpha1.AddToScheme(scheme)
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()

	ghMock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	}))
	defer ghMock.Close()

	ghClient := NewClient("dummy-token")
	ghClient.baseURL = ghMock.URL

	receiver := &WebhookReceiver{
		Client:        fakeClient,
		Scheme:        scheme,
		WebhookSecret: "secret",
		GHClient:      ghClient,
		Domain:        "test.com",
		GitopsPath:    "k8s",
		Namespace:     "default",
	}

	payload := PullRequestPayload{
		Action: "opened",
		Number: 1,
		PullRequest: PullRequestInfo{
			Number: 1,
			State:  "open",
			Head: HeadInfo{
				Ref: "feature-branch",
				Sha: "abcdef123456",
			},
		},
		Repository: RepositoryInfo{
			Name: "test-repo",
			Owner: OwnerInfo{
				Login: "test-owner",
			},
		},
	}
	body, _ := json.Marshal(payload)

	mac := hmac.New(sha256.New, []byte("secret"))
	mac.Write(body)
	sig := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	t.Run("Valid pull request opened event", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/webhook", bytes.NewReader(body))
		req.Header.Set("X-GitHub-Event", "pull_request")
		req.Header.Set("X-Hub-Signature-256", sig)

		rr := httptest.NewRecorder()
		receiver.handleWebhook(rr, req)

		if rr.Code != http.StatusOK {
			t.Errorf("expected status 200, got %d", rr.Code)
		}

		// Verify CR was created
		var env previewv1alpha1.PreviewEnvironment
		err := fakeClient.Get(context.Background(), types.NamespacedName{Name: "pr-1", Namespace: "default"}, &env)
		if err != nil {
			t.Fatalf("failed to find created CR: %v", err)
		}
		if env.Spec.CommitSha != "abcdef123456" {
			t.Errorf("expected commit SHA abcdef123456, got %s", env.Spec.CommitSha)
		}
	})

	t.Run("Non pull request event is ignored", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/webhook", bytes.NewReader(body))
		req.Header.Set("X-GitHub-Event", "ping")
		req.Header.Set("X-Hub-Signature-256", sig)

		rr := httptest.NewRecorder()
		receiver.handleWebhook(rr, req)

		if rr.Code != http.StatusOK {
			t.Errorf("expected status 200 for ignored event, got %d", rr.Code)
		}
	})

	t.Run("Invalid signature returns 401", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/webhook", bytes.NewReader(body))
		req.Header.Set("X-GitHub-Event", "pull_request")
		req.Header.Set("X-Hub-Signature-256", "sha256=wrong")

		rr := httptest.NewRecorder()
		receiver.handleWebhook(rr, req)

		if rr.Code != http.StatusUnauthorized {
			t.Errorf("expected status 401, got %d", rr.Code)
		}
	})

	t.Run("Non POST method returns 405", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/webhook", nil)
		rr := httptest.NewRecorder()
		receiver.handleWebhook(rr, req)

		if rr.Code != http.StatusMethodNotAllowed {
			t.Errorf("expected status 405, got %d", rr.Code)
		}
	})
}

func TestWebhookReceiver_ProcessPayload(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = previewv1alpha1.AddToScheme(scheme)
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()

	ghMock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	}))
	defer ghMock.Close()

	ghClient := NewClient("dummy-token")
	ghClient.baseURL = ghMock.URL

	receiver := &WebhookReceiver{
		Client:        fakeClient,
		Scheme:        scheme,
		WebhookSecret: "",
		GHClient:      ghClient,
		Domain:        "test.com",
		GitopsPath:    "k8s",
		Namespace:     "default",
	}

	ctx := context.Background()

	t.Run("Create PreviewEnvironment on opened", func(t *testing.T) {
		payload := PullRequestPayload{
			Action: "opened",
			Number: 42,
			PullRequest: PullRequestInfo{
				Number: 42,
				State:  "open",
				Head: HeadInfo{
					Ref: "feature-branch",
					Sha: "sha-1",
				},
			},
			Repository: RepositoryInfo{
				Name: "test-repo",
				Owner: OwnerInfo{
					Login: "test-owner",
				},
			},
		}

		err := receiver.processPayload(ctx, payload)
		if err != nil {
			t.Fatalf("failed to process payload: %v", err)
		}

		var env previewv1alpha1.PreviewEnvironment
		err = fakeClient.Get(ctx, types.NamespacedName{Name: "pr-42", Namespace: "default"}, &env)
		if err != nil {
			t.Fatalf("CR not found in fake cluster: %v", err)
		}
		if env.Spec.CommitSha != "sha-1" {
			t.Errorf("expected commit SHA sha-1, got %s", env.Spec.CommitSha)
		}
	})

	t.Run("Update PreviewEnvironment on synchronize", func(t *testing.T) {
		payload := PullRequestPayload{
			Action: "synchronize",
			Number: 42,
			PullRequest: PullRequestInfo{
				Number: 42,
				State:  "open",
				Head: HeadInfo{
					Ref: "feature-branch",
					Sha: "sha-2",
				},
			},
			Repository: RepositoryInfo{
				Name: "test-repo",
				Owner: OwnerInfo{
					Login: "test-owner",
				},
			},
		}

		err := receiver.processPayload(ctx, payload)
		if err != nil {
			t.Fatalf("failed to process payload: %v", err)
		}

		var env previewv1alpha1.PreviewEnvironment
		err = fakeClient.Get(ctx, types.NamespacedName{Name: "pr-42", Namespace: "default"}, &env)
		if err != nil {
			t.Fatalf("CR not found in fake cluster: %v", err)
		}
		if env.Spec.CommitSha != "sha-2" {
			t.Errorf("expected updated commit SHA sha-2, got %s", env.Spec.CommitSha)
		}
	})

	t.Run("Delete PreviewEnvironment on closed", func(t *testing.T) {
		payload := PullRequestPayload{
			Action: "closed",
			Number: 42,
			PullRequest: PullRequestInfo{
				Number: 42,
				State:  "closed",
				Head: HeadInfo{
					Ref: "feature-branch",
					Sha: "sha-2",
				},
			},
			Repository: RepositoryInfo{
				Name: "test-repo",
				Owner: OwnerInfo{
					Login: "test-owner",
				},
			},
		}

		err := receiver.processPayload(ctx, payload)
		if err != nil {
			t.Fatalf("failed to process payload: %v", err)
		}

		var env previewv1alpha1.PreviewEnvironment
		err = fakeClient.Get(ctx, types.NamespacedName{Name: "pr-42", Namespace: "default"}, &env)
		if err == nil {
			t.Fatal("expected CR to be deleted, but it was found")
		}
	})
}
