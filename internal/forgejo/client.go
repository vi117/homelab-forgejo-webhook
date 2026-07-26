package forgejo

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

type Client struct {
	baseURL *url.URL
	http    *http.Client
}

type Repository struct {
	Name          string `json:"name"`
	FullName      string `json:"full_name"`
	DefaultBranch string `json:"default_branch"`
	Private       bool   `json:"private"`
	Empty         bool   `json:"empty"`
	CloneURL      string `json:"clone_url"`
	Owner         struct {
		Login    string `json:"login"`
		Username string `json:"username"`
	} `json:"owner"`
}

func NewClient(baseURL string, client *http.Client) (*Client, error) {
	parsed, err := parseHTTPURL(baseURL)
	if err != nil {
		return nil, fmt.Errorf("Forgejo API URL: %w", err)
	}
	if client == nil {
		client = http.DefaultClient
	}
	return &Client{baseURL: parsed, http: client}, nil
}

func (client *Client) Repository(ctx context.Context, owner, name string) (Repository, error) {
	target := *client.baseURL
	target.Path = strings.TrimRight(target.Path, "/") + "/api/v1/repos/" +
		url.PathEscape(owner) + "/" + url.PathEscape(name)

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return Repository{}, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "homelab-forgejo-webhook/1")

	response, err := client.http.Do(request)
	if err != nil {
		return Repository{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return Repository{}, fmt.Errorf("Forgejo API returned %s", response.Status)
	}

	var repository Repository
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&repository); err != nil {
		return Repository{}, fmt.Errorf("decode Forgejo repository: %w", err)
	}
	return repository, nil
}

func (client *Client) Ready(ctx context.Context) error {
	target := *client.baseURL
	target.Path = strings.TrimRight(target.Path, "/") + "/api/v1/version"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return err
	}
	response, err := client.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("Forgejo API returned %s", response.Status)
	}
	return nil
}

func parseHTTPURL(value string) (*url.URL, error) {
	parsed, err := url.Parse(value)
	if err != nil ||
		parsed.Host == "" ||
		(parsed.Scheme != "http" && parsed.Scheme != "https") ||
		parsed.User != nil ||
		parsed.RawQuery != "" ||
		parsed.Fragment != "" {
		return nil, fmt.Errorf("must be an absolute HTTP URL")
	}
	return parsed, nil
}
