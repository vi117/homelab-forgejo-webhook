package gitpages

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestReadyConnectsWithoutSendingRequest(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	result := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			result <- err
			return
		}
		defer conn.Close()
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		var buf [1]byte
		_, err = conn.Read(buf[:])
		result <- err
	}()
	client, err := NewClient("http://"+listener.Addr().String(), "pages.example.com", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Ready(context.Background()); err != nil {
		t.Fatalf("Ready() error = %v", err)
	}
	if err := <-result; err != io.EOF {
		t.Fatalf("read error = %v, want EOF without request bytes", err)
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	if err := client.Ready(context.Background()); err == nil {
		t.Fatal("Ready() succeeded with a closed listener")
	}
}

func TestReadyHonorsCanceledContext(t *testing.T) {
	client, err := NewClient("http://127.0.0.1:3000", "pages.example.com", nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := client.Ready(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Ready() error = %v, want context.Canceled", err)
	}
}

func TestDeployUpdatesAndConfirmsManifest(t *testing.T) {
	const commit = "1111111111111111111111111111111111111111"
	var mu sync.Mutex
	var deployed bool
	var receivedHost, receivedBranch, receivedURL string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPut && r.URL.Path == "/":
			body, _ := io.ReadAll(r.Body)
			mu.Lock()
			deployed = true
			receivedHost = r.Host
			receivedBranch = r.Header.Get("Branch")
			receivedURL = string(body)
			mu.Unlock()
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodGet && r.URL.Path == "/.git-pages/manifest.json":
			mu.Lock()
			ready := deployed
			mu.Unlock()
			if !ready {
				http.NotFound(w, r)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]string{
				"repoUrl": "https://forge.example.com/pages/site.git",
				"branch":  "main",
				"commit":  commit,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, err := NewClient(server.URL, "pages.example.com", server.Client())
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err = client.Deploy(ctx, Deployment{
		Repository: "site",
		Commit:     commit,
		Branch:     "main",
		CloneURL:   "https://forge.example.com/pages/site.git",
	})
	if err != nil {
		t.Fatalf("Deploy() error = %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if receivedHost != "site.pages.example.com" ||
		receivedBranch != "main" ||
		receivedURL != "https://forge.example.com/pages/site.git" {
		t.Fatalf("host=%q branch=%q URL=%q", receivedHost, receivedBranch, receivedURL)
	}
}

func TestDeploySkipsMatchingManifest(t *testing.T) {
	const commit = "1111111111111111111111111111111111111111"
	var putCount int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			putCount++
			w.WriteHeader(http.StatusOK)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{
			"repoUrl": "https://forge.example.com/pages/site.git",
			"branch":  "main",
			"commit":  commit,
		})
	}))
	defer server.Close()

	client, _ := NewClient(server.URL, "pages.example.com", server.Client())
	err := client.Deploy(context.Background(), Deployment{
		Repository: "site",
		Commit:     commit,
		Branch:     "main",
		CloneURL:   "https://forge.example.com/pages/site.git",
	})
	if err != nil {
		t.Fatalf("Deploy() error = %v", err)
	}
	if putCount != 0 {
		t.Fatalf("PUT count = %d, want 0", putCount)
	}
}
