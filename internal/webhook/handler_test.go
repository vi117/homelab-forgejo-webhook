package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vi117/homelab-forgejo-webhook/internal/forgejo"
	"github.com/vi117/homelab-forgejo-webhook/internal/gitpages"
)

const (
	testSecret = "0123456789abcdef0123456789abcdef"
	testCommit = "1111111111111111111111111111111111111111"
)

type forgejoStub struct {
	repository forgejo.Repository
	err        error
	calls      int
}

func (stub *forgejoStub) Repository(
	_ context.Context,
	_, _ string,
) (forgejo.Repository, error) {
	stub.calls++
	return stub.repository, stub.err
}

func (stub *forgejoStub) Ready(context.Context) error {
	return stub.err
}

type gitPagesStub struct {
	mu          sync.Mutex
	deployments []gitpages.Deployment
	err         error
}

func (stub *gitPagesStub) Deploy(_ context.Context, deployment gitpages.Deployment) error {
	stub.mu.Lock()
	defer stub.mu.Unlock()
	stub.deployments = append(stub.deployments, deployment)
	return stub.err
}

func (stub *gitPagesStub) Ready(context.Context) error {
	return stub.err
}

func newTestHandler(t *testing.T) (*Handler, *forgejoStub, *gitPagesStub) {
	t.Helper()
	repository := forgejo.Repository{
		Name:          "site",
		FullName:      "pages/site",
		DefaultBranch: "main",
		CloneURL:      "https://forge.example.com/pages/site.git",
	}
	repository.Owner.Username = "pages"
	forgejoClient := &forgejoStub{repository: repository}
	gitPagesClient := &gitPagesStub{}
	handler, err := New(Config{
		Owner:            "pages",
		ForgejoPublicURL: "https://forge.example.com",
		WebhookSecret:    []byte(testSecret),
		MaxWebhookBytes:  1 << 20,
		DeployTimeout:    time.Second,
		DeployAsync:      false,
		Forgejo:          forgejoClient,
		GitPages:         gitPagesClient,
		Logger:           slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return handler, forgejoClient, gitPagesClient
}

func webhookBody(owner, ref, commit string) []byte {
	body, _ := json.Marshal(map[string]any{
		"ref":   ref,
		"after": commit,
		"repository": map[string]any{
			"name":      "site",
			"full_name": owner + "/site",
			"private":   false,
			"owner": map[string]string{
				"username": owner,
			},
		},
	})
	return body
}

func signedRequest(body []byte) *http.Request {
	request := httptest.NewRequest(
		http.MethodPost,
		"/hooks/forgejo/site",
		bytes.NewReader(body),
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Forgejo-Event", "push")
	request.Header.Set("X-Forgejo-Delivery", "delivery-1")
	mac := hmac.New(sha256.New, []byte(testSecret))
	_, _ = mac.Write(body)
	request.Header.Set("X-Forgejo-Signature", hex.EncodeToString(mac.Sum(nil)))
	return request
}

func TestValidWebhookDeploysVerifiedRepository(t *testing.T) {
	handler, _, gitPagesClient := newTestHandler(t)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, signedRequest(webhookBody("pages", "refs/heads/main", testCommit)))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	gitPagesClient.mu.Lock()
	defer gitPagesClient.mu.Unlock()
	if len(gitPagesClient.deployments) != 1 {
		t.Fatalf("deployments = %d, want 1", len(gitPagesClient.deployments))
	}
	deployment := gitPagesClient.deployments[0]
	if deployment.Repository != "site" ||
		deployment.Branch != "main" ||
		deployment.Commit != testCommit ||
		deployment.CloneURL != "https://forge.example.com/pages/site.git" {
		t.Fatalf("deployment = %#v", deployment)
	}
}

func TestInvalidSignatureIsRejectedBeforeForgejoLookup(t *testing.T) {
	handler, forgejoClient, gitPagesClient := newTestHandler(t)
	request := signedRequest(webhookBody("pages", "refs/heads/main", testCommit))
	request.Header.Set("X-Forgejo-Signature", strings.Repeat("0", 64))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusUnauthorized)
	}
	if forgejoClient.calls != 0 || len(gitPagesClient.deployments) != 0 {
		t.Fatalf("Forgejo calls=%d deployments=%d", forgejoClient.calls, len(gitPagesClient.deployments))
	}
}

func TestRepositoryOutsideOwnerIsRejected(t *testing.T) {
	handler, _, _ := newTestHandler(t)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, signedRequest(webhookBody("other", "refs/heads/main", testCommit)))
	if response.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusForbidden)
	}
}

func TestNonDefaultBranchDoesNotDeploy(t *testing.T) {
	handler, _, gitPagesClient := newTestHandler(t)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, signedRequest(webhookBody("pages", "refs/heads/topic", testCommit)))
	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusAccepted)
	}
	if len(gitPagesClient.deployments) != 0 {
		t.Fatalf("deployments = %d, want 0", len(gitPagesClient.deployments))
	}
}

func TestDuplicateCommitIsIdempotent(t *testing.T) {
	handler, _, gitPagesClient := newTestHandler(t)
	body := webhookBody("pages", "refs/heads/main", testCommit)
	first := httptest.NewRecorder()
	handler.ServeHTTP(first, signedRequest(body))
	second := httptest.NewRecorder()
	handler.ServeHTTP(second, signedRequest(body))

	if first.Code != http.StatusOK || second.Code != http.StatusOK {
		t.Fatalf("statuses = %d, %d", first.Code, second.Code)
	}
	if !strings.Contains(second.Body.String(), "duplicate") {
		t.Fatalf("second body = %s", second.Body.String())
	}
	if len(gitPagesClient.deployments) != 1 {
		t.Fatalf("deployments = %d, want 1", len(gitPagesClient.deployments))
	}
}

func TestReservedRepositoryIsRejected(t *testing.T) {
	handler, _, _ := newTestHandler(t)
	body := webhookBody("pages", "refs/heads/main", testCommit)
	request := signedRequest(body)
	request.URL.Path = "/hooks/forgejo/www"
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusBadRequest)
	}
}

func TestHealthReadyAndMetrics(t *testing.T) {
	handler, _, _ := newTestHandler(t)
	for _, test := range []struct {
		path string
		body string
	}{
		{path: "/healthz", body: "ok"},
		{path: "/readyz", body: "ready"},
		{path: "/metrics", body: "pages_deployments_total"},
	} {
		t.Run(test.path, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, test.path, nil))
			if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), test.body) {
				t.Fatalf("status=%d body=%q", response.Code, response.Body.String())
			}
		})
	}
}

func TestWaitReturnsAfterAsyncDeployment(t *testing.T) {
	handler, _, _ := newTestHandler(t)
	handler.cfg.DeployAsync = true
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, signedRequest(webhookBody("pages", "refs/heads/main", testCommit)))
	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := handler.Wait(ctx); err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
}
