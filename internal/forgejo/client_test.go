package forgejo

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRepositoryUsesForgejoAPI(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/repos/pages/site" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"name":"site","full_name":"pages/site","default_branch":"main","clone_url":"https://forge.example.com/pages/site.git","owner":{"username":"pages"}}`))
	}))
	defer server.Close()

	client, err := NewClient(server.URL, server.Client())
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	repository, err := client.Repository(context.Background(), "pages", "site")
	if err != nil {
		t.Fatalf("Repository() error = %v", err)
	}
	if repository.FullName != "pages/site" || repository.Owner.Username != "pages" {
		t.Fatalf("Repository() = %#v", repository)
	}
}
