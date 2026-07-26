package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/vi117/homelab-forgejo-webhook/internal/forgejo"
	"github.com/vi117/homelab-forgejo-webhook/internal/gitpages"
)

var (
	repositoryNamePattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)
	commitPattern         = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)
	reservedNames         = map[string]struct{}{
		"admin": {}, "api": {}, "git": {}, "healthz": {}, "metrics": {},
		"pages": {}, "readyz": {}, "status": {}, "www": {},
	}
)

type ForgejoClient interface {
	Repository(context.Context, string, string) (forgejo.Repository, error)
	Ready(context.Context) error
}

type GitPagesClient interface {
	Deploy(context.Context, gitpages.Deployment) error
	Ready(context.Context) error
}

type Config struct {
	Owner            string
	ForgejoPublicURL string
	WebhookSecret    []byte
	MaxWebhookBytes  int64
	DeployTimeout    time.Duration
	DeployAsync      bool
	Forgejo          ForgejoClient
	GitPages         GitPagesClient
	Logger           *slog.Logger
}

type Handler struct {
	cfg              Config
	forgejoPublicURL *url.URL
	repositoryLocks  sync.Map
	queueMu          sync.Mutex
	queued           map[string]string
	lastSuccessful   map[string]string
	deployWG         sync.WaitGroup
	metrics          metrics
}

type pushPayload struct {
	Ref        string `json:"ref"`
	After      string `json:"after"`
	Repository struct {
		Name     string `json:"name"`
		FullName string `json:"full_name"`
		Private  bool   `json:"private"`
		Owner    struct {
			Login    string `json:"login"`
			Username string `json:"username"`
		} `json:"owner"`
	} `json:"repository"`
}

type deployment struct {
	gitpages.Deployment
	DeliveryID string
}

func New(cfg Config) (*Handler, error) {
	if !repositoryNamePattern.MatchString(cfg.Owner) {
		return nil, errors.New("owner must be a lowercase DNS label")
	}
	if len(cfg.WebhookSecret) < 32 {
		return nil, errors.New("webhook secret must contain at least 32 bytes")
	}
	if cfg.MaxWebhookBytes <= 0 {
		return nil, errors.New("maximum webhook size must be positive")
	}
	if cfg.DeployTimeout <= 0 {
		return nil, errors.New("deployment timeout must be positive")
	}
	if cfg.Forgejo == nil || cfg.GitPages == nil {
		return nil, errors.New("Forgejo and git-pages clients are required")
	}
	publicURL, err := parseHTTPURL(cfg.ForgejoPublicURL)
	if err != nil {
		return nil, fmt.Errorf("Forgejo public URL: %w", err)
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.NewJSONHandler(os.Stdout, nil))
	}
	return &Handler{
		cfg:              cfg,
		forgejoPublicURL: publicURL,
		queued:           make(map[string]string),
		lastSuccessful:   make(map[string]string),
	}, nil
}

func (handler *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/healthz":
		handler.health(w, r)
	case r.URL.Path == "/readyz":
		handler.ready(w, r)
	case r.URL.Path == "/metrics":
		handler.metrics.serve(w, r)
	case strings.HasPrefix(r.URL.Path, "/hooks/forgejo/"):
		handler.webhook(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (handler *Handler) health(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, "ok\n")
}

func (handler *Handler) ready(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := handler.cfg.Forgejo.Ready(ctx); err != nil {
		http.Error(w, "forgejo unavailable", http.StatusServiceUnavailable)
		return
	}
	if err := handler.cfg.GitPages.Ready(ctx); err != nil {
		http.Error(w, "git-pages unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, "ready\n")
}

func (handler *Handler) webhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	repository := strings.TrimPrefix(r.URL.Path, "/hooks/forgejo/")
	if !repositoryNamePattern.MatchString(repository) || strings.Contains(repository, "/") {
		handler.reject(w, "invalid repository name", http.StatusBadRequest)
		return
	}
	if _, reserved := reservedNames[repository]; reserved {
		handler.reject(w, "reserved repository name", http.StatusBadRequest)
		return
	}

	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		handler.reject(w, "content type must be application/json", http.StatusUnsupportedMediaType)
		return
	}
	if r.Header.Get("X-Forgejo-Event") != "push" {
		handler.reject(w, "only Forgejo push events are accepted", http.StatusBadRequest)
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, handler.cfg.MaxWebhookBytes))
	if err != nil {
		handler.reject(w, "webhook body is too large", http.StatusRequestEntityTooLarge)
		return
	}
	if !verifySignature(body, r.Header.Get("X-Forgejo-Signature"), handler.cfg.WebhookSecret) {
		handler.reject(w, "invalid webhook signature", http.StatusUnauthorized)
		return
	}

	var payload pushPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		handler.reject(w, "invalid webhook payload", http.StatusBadRequest)
		return
	}

	payloadOwner := ownerName(payload.Repository.Owner.Username, payload.Repository.Owner.Login)
	expectedFullName := handler.cfg.Owner + "/" + repository
	if payloadOwner != handler.cfg.Owner ||
		payload.Repository.Name != repository ||
		payload.Repository.FullName != expectedFullName {
		handler.reject(w, "repository is outside the configured owner", http.StatusForbidden)
		return
	}
	if !commitPattern.MatchString(payload.After) || isZeroCommit(payload.After) {
		handler.reject(w, "invalid target commit", http.StatusBadRequest)
		return
	}

	repositoryInfo, err := handler.cfg.Forgejo.Repository(r.Context(), handler.cfg.Owner, repository)
	if err != nil {
		handler.cfg.Logger.Warn("Forgejo repository verification failed",
			"repository", expectedFullName, "error", err)
		handler.reject(w, "repository verification failed", http.StatusBadGateway)
		return
	}
	apiOwner := ownerName(repositoryInfo.Owner.Username, repositoryInfo.Owner.Login)
	if apiOwner != handler.cfg.Owner ||
		repositoryInfo.Name != repository ||
		repositoryInfo.FullName != expectedFullName {
		handler.reject(w, "Forgejo API repository mismatch", http.StatusForbidden)
		return
	}
	if repositoryInfo.Private || payload.Repository.Private {
		handler.reject(w, "private repositories are not supported", http.StatusForbidden)
		return
	}
	if repositoryInfo.Empty || repositoryInfo.DefaultBranch == "" {
		handler.reject(w, "repository has no deployable default branch", http.StatusConflict)
		return
	}
	if payload.Ref != "refs/heads/"+repositoryInfo.DefaultBranch {
		handler.reject(w, "push is not for the default branch", http.StatusAccepted)
		return
	}

	cloneURL := handler.cloneURL(repository)
	if !sameURL(repositoryInfo.CloneURL, cloneURL) {
		handler.reject(w, "Forgejo API clone URL does not match the configured origin", http.StatusForbidden)
		return
	}

	job := deployment{
		Deployment: gitpages.Deployment{
			Repository: repository,
			Commit:     payload.After,
			Branch:     repositoryInfo.DefaultBranch,
			CloneURL:   cloneURL,
		},
		DeliveryID: r.Header.Get("X-Forgejo-Delivery"),
	}
	if !handler.enqueue(job) {
		handler.metrics.duplicates.Add(1)
		writeJSON(w, http.StatusOK, map[string]string{"result": "duplicate"})
		return
	}

	handler.metrics.accepted.Add(1)
	if handler.cfg.DeployAsync {
		handler.deployWG.Add(1)
		go func() {
			defer handler.deployWG.Done()
			_ = handler.runDeployment(job)
		}()
		writeJSON(w, http.StatusAccepted, map[string]string{"result": "queued"})
		return
	}
	if err := handler.runDeployment(job); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"result": "failed"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"result": "deployed"})
}

func (handler *Handler) cloneURL(repository string) string {
	target := *handler.forgejoPublicURL
	target.Path = strings.TrimRight(target.Path, "/") + "/" +
		handler.cfg.Owner + "/" + repository + ".git"
	return target.String()
}

func (handler *Handler) enqueue(job deployment) bool {
	handler.queueMu.Lock()
	defer handler.queueMu.Unlock()
	if handler.queued[job.Repository] == job.Commit ||
		handler.lastSuccessful[job.Repository] == job.Commit {
		return false
	}
	handler.queued[job.Repository] = job.Commit
	return true
}

func (handler *Handler) runDeployment(job deployment) error {
	lockValue, _ := handler.repositoryLocks.LoadOrStore(job.Repository, &sync.Mutex{})
	lock := lockValue.(*sync.Mutex)
	lock.Lock()
	defer lock.Unlock()

	started := time.Now()
	handler.metrics.deployInFlight.Add(1)
	defer handler.metrics.deployInFlight.Add(-1)

	ctx, cancel := context.WithTimeout(context.Background(), handler.cfg.DeployTimeout)
	defer cancel()
	err := handler.cfg.GitPages.Deploy(ctx, job.Deployment)
	duration := time.Since(started)
	handler.metrics.setLastDuration(duration)

	handler.queueMu.Lock()
	if handler.queued[job.Repository] == job.Commit {
		delete(handler.queued, job.Repository)
	}
	if err == nil {
		handler.lastSuccessful[job.Repository] = job.Commit
	}
	handler.queueMu.Unlock()

	logValues := []any{
		"repository", handler.cfg.Owner + "/" + job.Repository,
		"commit", job.Commit,
		"delivery_id", job.DeliveryID,
		"duration_ms", duration.Milliseconds(),
	}
	if err != nil {
		handler.metrics.deployFailure.Add(1)
		handler.cfg.Logger.Error("site deployment failed",
			append(logValues, "result", "failure", "error", err)...)
		return err
	}
	handler.metrics.deploySuccess.Add(1)
	handler.metrics.lastSuccessUnix.Store(time.Now().Unix())
	handler.cfg.Logger.Info("site deployment completed",
		append(logValues, "result", "success")...)
	return nil
}

func (handler *Handler) reject(w http.ResponseWriter, message string, status int) {
	handler.metrics.rejected.Add(1)
	writeJSON(w, status, map[string]string{"error": message})
}

func (handler *Handler) Wait(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		handler.deployWG.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func verifySignature(payload []byte, header string, secret []byte) bool {
	header = strings.TrimPrefix(strings.TrimSpace(header), "sha256=")
	provided, err := hex.DecodeString(header)
	if err != nil || len(provided) != sha256.Size {
		return false
	}
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(payload)
	expected := mac.Sum(nil)
	if len(provided) != len(expected) {
		return false
	}
	return subtle.ConstantTimeCompare(provided, expected) == 1
}

func ownerName(username, login string) string {
	if username != "" {
		return username
	}
	return login
}

func isZeroCommit(value string) bool {
	for _, char := range value {
		if char != '0' {
			return false
		}
	}
	return true
}

func sameURL(left, right string) bool {
	leftURL, leftErr := url.Parse(left)
	rightURL, rightErr := url.Parse(right)
	if leftErr != nil || rightErr != nil {
		return false
	}
	return strings.EqualFold(leftURL.Scheme, rightURL.Scheme) &&
		strings.EqualFold(leftURL.Host, rightURL.Host) &&
		strings.TrimSuffix(leftURL.Path, "/") == strings.TrimSuffix(rightURL.Path, "/") &&
		leftURL.User == nil && rightURL.User == nil &&
		leftURL.Fragment == "" && rightURL.Fragment == "" &&
		leftURL.RawQuery == "" && rightURL.RawQuery == ""
}

func parseHTTPURL(value string) (*url.URL, error) {
	parsed, err := url.Parse(value)
	if err != nil ||
		parsed.Host == "" ||
		(parsed.Scheme != "http" && parsed.Scheme != "https") ||
		parsed.User != nil ||
		parsed.RawQuery != "" ||
		parsed.Fragment != "" {
		return nil, errors.New("must be an absolute HTTP URL")
	}
	return parsed, nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
