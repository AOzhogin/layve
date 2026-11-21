// Package dockerhub is a small client for the public Docker Hub HTTP API:
// repository search and tag listing with per-platform builds.
package dockerhub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultBaseURL is the Docker Hub API.
const DefaultBaseURL = "https://hub.docker.com"

// ErrNotFound is returned for repositories that do not exist.
var ErrNotFound = errors.New("not found on Docker Hub")

// Client talks to the Docker Hub API anonymously.
type Client struct {
	base string
	http *http.Client
}

// New returns a client; an empty base means DefaultBaseURL.
func New(base string, hc *http.Client) *Client {
	if base == "" {
		base = DefaultBaseURL
	}
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{base: strings.TrimSuffix(base, "/"), http: hc}
}

// Repository is a search result.
type Repository struct {
	Name        string `json:"repo_name"`
	Description string `json:"short_description"`
	Stars       int    `json:"star_count"`
	Pulls       int64  `json:"pull_count"`
	Official    bool   `json:"is_official"`
}

// Search finds repositories matching query, best matches first.
func (c *Client) Search(ctx context.Context, query string, limit int) ([]Repository, error) {
	q := url.Values{"query": {query}, "page_size": {fmt.Sprint(limit)}}
	var res struct {
		Results []Repository `json:"results"`
	}
	if err := c.get(ctx, "/v2/search/repositories/?"+q.Encode(), &res); err != nil {
		return nil, err
	}
	return res.Results, nil
}

// Image is one platform build of a tag.
type Image struct {
	OS           string `json:"os"`
	Architecture string `json:"architecture"`
	Variant      string `json:"variant"`
	Size         int64  `json:"size"`
	Digest       string `json:"digest"`
}

// Tag is a tag of a repository.
type Tag struct {
	Name        string    `json:"name"`
	LastUpdated time.Time `json:"last_updated"`
	Images      []Image   `json:"images"`
}

// Tags lists the most recently pushed tags of repo ("nginx" or
// "bitnami/redis"), optionally only those whose name contains filter.
func (c *Client) Tags(ctx context.Context, repo, filter string, limit int) ([]Tag, error) {
	q := url.Values{"page_size": {fmt.Sprint(limit)}, "ordering": {"last_updated"}}
	if filter != "" {
		q.Set("name", filter)
	}
	var res struct {
		Results []Tag `json:"results"`
	}
	path := "/v2/repositories/" + Namespaced(repo) + "/tags?" + q.Encode()
	if err := c.get(ctx, path, &res); err != nil {
		return nil, err
	}
	return res.Results, nil
}

// Namespaced turns official image names into their Hub path: "nginx"
// becomes "library/nginx"; "docker.io/" prefixes are dropped.
func Namespaced(repo string) string {
	repo = strings.TrimPrefix(strings.TrimPrefix(repo, "docker.io/"), "index.docker.io/")
	if !strings.Contains(repo, "/") {
		return "library/" + repo
	}
	return repo
}

func (c *Client) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("docker hub: %w", err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return ErrNotFound
	case resp.StatusCode == http.StatusTooManyRequests:
		return errors.New("docker hub: rate limit reached, try again in a minute")
	case resp.StatusCode != http.StatusOK:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("docker hub: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("docker hub: decode response: %w", err)
	}
	return nil
}
