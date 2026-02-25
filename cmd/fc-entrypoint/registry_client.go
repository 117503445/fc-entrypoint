package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
)

const (
	defaultBufferSize = 64 * 1024 // 64KB buffer for streaming
	maxRetries        = 3
	retryDelay        = time.Second
)

// ManifestV2 represents a Docker manifest schema v2
type ManifestV2 struct {
	SchemaVersion int    `json:"schemaVersion"`
	MediaType     string `json:"mediaType"`
	Config        struct {
		MediaType string `json:"mediaType"`
		Size      int64  `json:"size"`
		Digest    string `json:"digest"`
	} `json:"config"`
	Layers []struct {
		MediaType string `json:"mediaType"`
		Size      int64  `json:"size"`
		Digest    string `json:"digest"`
	} `json:"layers"`
}

// ParseImageReference parses an image reference string into ImageInfo
// Format: registry/repository:tag or registry/repository@digest
func ParseImageReference(image string) (*ImageInfo, error) {
	info := &ImageInfo{}

	// Split by @ for digest
	if strings.Contains(image, "@") {
		parts := strings.SplitN(image, "@", 2)
		image = parts[0]
		info.Digest = parts[1]
	}

	// Split by : for tag
	tagIndex := strings.LastIndex(image, ":")
	if tagIndex != -1 && !strings.Contains(image[tagIndex:], "/") {
		info.Tag = image[tagIndex+1:]
		image = image[:tagIndex]
	} else {
		info.Tag = "latest"
	}

	// Split registry and repository
	slashIndex := strings.Index(image, "/")
	if slashIndex == -1 {
		// No slash, assume docker.io/library
		info.Registry = "registry-1.docker.io"
		info.Repository = "library/" + image
	} else {
		firstPart := image[:slashIndex]
		// Check if first part is a registry (contains . or :)
		if strings.Contains(firstPart, ".") || strings.Contains(firstPart, ":") {
			info.Registry = firstPart
			info.Repository = image[slashIndex+1:]
		} else {
			// No registry specified, assume docker.io
			info.Registry = "registry-1.docker.io"
			info.Repository = image
		}
	}

	return info, nil
}

// RegistryClient handles communication with Docker registries
type RegistryClient struct {
	ctx        context.Context
	httpClient *http.Client
	username   string
	password   string
	token      string
}

// NewRegistryClient creates a new registry client
func NewRegistryClient(ctx context.Context, username, password string) *RegistryClient {
	return &RegistryClient{
		ctx: ctx,
		httpClient: &http.Client{
			Timeout: 10 * time.Minute,
		},
		username: username,
		password: password,
	}
}

// PullManifest fetches the manifest for an image
func (c *RegistryClient) PullManifest(info *ImageInfo) error {
	url := fmt.Sprintf("https://%s/v2/%s/manifests/%s", info.Registry, info.Repository, info.Tag)

	req, err := http.NewRequestWithContext(c.ctx, "GET", url, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	// Accept both Docker manifest v2 and OCI manifest formats
	req.Header.Add("Accept", "application/vnd.docker.distribution.manifest.v2+json")
	req.Header.Add("Accept", "application/vnd.oci.image.manifest.v1+json")

	c.setAuth(req)

	resp, err := c.doWithRetry(req)
	if err != nil {
		return fmt.Errorf("failed to fetch manifest: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		// Try to get token and retry
		if err := c.handleAuth(resp, info); err != nil {
			return fmt.Errorf("authentication failed: %w", err)
		}
		// Retry with token
		req, _ = http.NewRequestWithContext(c.ctx, "GET", url, nil)
		req.Header.Add("Accept", "application/vnd.docker.distribution.manifest.v2+json")
		req.Header.Add("Accept", "application/vnd.oci.image.manifest.v1+json")
		c.setAuth(req)
		resp, err = c.doWithRetry(req)
		if err != nil {
			return fmt.Errorf("failed to fetch manifest after auth: %w", err)
		}
		defer resp.Body.Close()
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("manifest request failed with status %d: %s", resp.StatusCode, string(body))
	}

	var manifest ManifestV2
	if err := json.NewDecoder(resp.Body).Decode(&manifest); err != nil {
		return fmt.Errorf("failed to decode manifest: %w", err)
	}

	// Set digest from config
	info.Digest = manifest.Config.Digest

	// Extract layer info
	info.Layers = make([]LayerInfo, len(manifest.Layers))
	for i, layer := range manifest.Layers {
		info.Layers[i] = LayerInfo{
			Digest: layer.Digest,
			Size:   layer.Size,
		}
	}

	log.Ctx(c.ctx).Info().
		Str("registry", info.Registry).
		Str("repository", info.Repository).
		Str("tag", info.Tag).
		Int("layers", len(info.Layers)).
		Msg("Fetched manifest")

	return nil
}

// DownloadLayer downloads a layer to the specified directory
func (c *RegistryClient) DownloadLayer(info *ImageInfo, layer *LayerInfo, destDir string) error {
	// Check if already downloaded
	localPath := filepath.Join(destDir, strings.Replace(layer.Digest, ":", "_", 1)+".tar.gz")
	if _, err := os.Stat(localPath); err == nil {
		// Verify checksum
		if c.verifyChecksum(localPath, layer.Digest) {
			layer.LocalPath = localPath
			log.Ctx(c.ctx).Debug().Str("digest", layer.Digest).Msg("Layer already exists, skipping download")
			return nil
		}
		// Checksum mismatch, remove and re-download
		os.Remove(localPath)
	}

	url := fmt.Sprintf("https://%s/v2/%s/blobs/%s", info.Registry, info.Repository, layer.Digest)

	req, err := http.NewRequestWithContext(c.ctx, "GET", url, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	c.setAuth(req)

	resp, err := c.doWithRetry(req)
	if err != nil {
		return fmt.Errorf("failed to download layer: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		if err := c.handleAuth(resp, info); err != nil {
			return fmt.Errorf("authentication failed: %w", err)
		}
		req, _ = http.NewRequestWithContext(c.ctx, "GET", url, nil)
		c.setAuth(req)
		resp, err = c.doWithRetry(req)
		if err != nil {
			return fmt.Errorf("failed to download layer after auth: %w", err)
		}
		defer resp.Body.Close()
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("layer download failed with status %d: %s", resp.StatusCode, string(body))
	}

	// Create destination directory
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	// Create temp file for download
	tmpPath := localPath + ".tmp"
	file, err := os.Create(tmpPath)
	if err != nil {
		return fmt.Errorf("failed to create file: %w", err)
	}

	// Stream download with checksum verification
	hash := sha256.New()
	writer := io.MultiWriter(file, hash)

	buf := make([]byte, defaultBufferSize)
	_, err = io.CopyBuffer(writer, resp.Body, buf)
	file.Close()

	if err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("failed to download layer: %w", err)
	}

	// Verify checksum
	computedDigest := "sha256:" + hex.EncodeToString(hash.Sum(nil))
	if computedDigest != layer.Digest {
		os.Remove(tmpPath)
		return fmt.Errorf("checksum mismatch: expected %s, got %s", layer.Digest, computedDigest)
	}

	// Rename to final path
	if err := os.Rename(tmpPath, localPath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("failed to rename file: %w", err)
	}

	layer.LocalPath = localPath
	log.Ctx(c.ctx).Info().
		Str("digest", layer.Digest).
		Int64("size", layer.Size).
		Msg("Downloaded layer")

	return nil
}

// setAuth sets authentication header on request
func (c *RegistryClient) setAuth(req *http.Request) {
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	} else if c.username != "" && c.password != "" {
		auth := base64.StdEncoding.EncodeToString([]byte(c.username + ":" + c.password))
		req.Header.Set("Authorization", "Basic "+auth)
	}
}

// handleAuth handles authentication challenge
func (c *RegistryClient) handleAuth(resp *http.Response, info *ImageInfo) error {
	authHeader := resp.Header.Get("Www-Authenticate")
	if authHeader == "" {
		return fmt.Errorf("no Www-Authenticate header in 401 response")
	}

	// Parse Bearer realm="...",service="...",scope="..."
	if !strings.HasPrefix(authHeader, "Bearer ") {
		return fmt.Errorf("unsupported auth type: %s", authHeader)
	}

	params := parseAuthParams(authHeader[7:])
	realm := params["realm"]
	service := params["service"]
	scope := params["scope"]

	if realm == "" {
		return fmt.Errorf("missing realm in auth header")
	}

	// Request token
	tokenURL := realm
	if service != "" || scope != "" {
		tokenURL += "?"
		if service != "" {
			tokenURL += "service=" + service
		}
		if scope != "" {
			if service != "" {
				tokenURL += "&"
			}
			tokenURL += "scope=" + scope
		}
	}

	req, err := http.NewRequestWithContext(c.ctx, "GET", tokenURL, nil)
	if err != nil {
		return err
	}

	// Use basic auth for token request if credentials provided
	if c.username != "" && c.password != "" {
		auth := base64.StdEncoding.EncodeToString([]byte(c.username + ":" + c.password))
		req.Header.Set("Authorization", "Basic "+auth)
	}

	tokenResp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("token request failed: %w", err)
	}
	defer tokenResp.Body.Close()

	if tokenResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(tokenResp.Body)
		return fmt.Errorf("token request failed with status %d: %s", tokenResp.StatusCode, string(body))
	}

	var tokenData struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(tokenResp.Body).Decode(&tokenData); err != nil {
		return fmt.Errorf("failed to decode token response: %w", err)
	}

	token := tokenData.Token
	if token == "" {
		token = tokenData.AccessToken
	}

	// Store token for subsequent requests
	c.token = token

	return nil
}

// parseAuthParams parses key="value" pairs from auth header
func parseAuthParams(s string) map[string]string {
	params := make(map[string]string)
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		idx := strings.Index(part, "=")
		if idx == -1 {
			continue
		}
		key := part[:idx]
		value := strings.Trim(part[idx+1:], "\"")
		params[key] = value
	}
	return params
}

// doWithRetry performs HTTP request with retry logic
func (c *RegistryClient) doWithRetry(req *http.Request) (*http.Response, error) {
	var lastErr error
	for i := 0; i < maxRetries; i++ {
		resp, err := c.httpClient.Do(req)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		log.Ctx(c.ctx).Warn().Err(err).Int("attempt", i+1).Msg("Request failed, retrying")
		time.Sleep(retryDelay * time.Duration(i+1))
	}
	return nil, lastErr
}

// verifyChecksum verifies file checksum
func (c *RegistryClient) verifyChecksum(path, expectedDigest string) bool {
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()

	hash := sha256.New()
	buf := make([]byte, defaultBufferSize)
	if _, err := io.CopyBuffer(hash, file, buf); err != nil {
		return false
	}

	computedDigest := "sha256:" + hex.EncodeToString(hash.Sum(nil))
	return computedDigest == expectedDigest
}

// GetImageDigest returns a short form of the image digest for directory naming
func GetImageDigest(info *ImageInfo) string {
	// Use config digest without "sha256:" prefix
	digest := info.Digest
	if strings.HasPrefix(digest, "sha256:") {
		digest = digest[7:]
	}
	return digest
}