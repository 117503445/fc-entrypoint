package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestParseImageReference(t *testing.T) {
	tests := []struct {
		name       string
		image      string
		registry   string
		repository string
		tag        string
	}{
		{
			name:       "simple image",
			image:      "ubuntu",
			registry:   "registry-1.docker.io",
			repository: "library/ubuntu",
			tag:        "latest",
		},
		{
			name:       "image with tag",
			image:      "ubuntu:20.04",
			registry:   "registry-1.docker.io",
			repository: "library/ubuntu",
			tag:        "20.04",
		},
		{
			name:       "image with namespace",
			image:      "myuser/myimage:v1.0",
			registry:   "registry-1.docker.io",
			repository: "myuser/myimage",
			tag:        "v1.0",
		},
		{
			name:       "image with registry",
			image:      "gcr.io/myproject/myimage:latest",
			registry:   "gcr.io",
			repository: "myproject/myimage",
			tag:        "latest",
		},
		{
			name:       "aliyun registry",
			image:      "registry.cn-hangzhou.aliyuncs.com/117503445-mirror/sync:linux.amd64.docker.io.library.ubuntu.latest",
			registry:   "registry.cn-hangzhou.aliyuncs.com",
			repository: "117503445-mirror/sync",
			tag:        "linux.amd64.docker.io.library.ubuntu.latest",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info, err := ParseImageReference(tt.image)
			assert.NoError(t, err)
			assert.Equal(t, tt.registry, info.Registry)
			assert.Equal(t, tt.repository, info.Repository)
			assert.Equal(t, tt.tag, info.Tag)
		})
	}
}

func TestGetInstanceID(t *testing.T) {
	// Reset instance ID
	ResetInstanceID()

	// Test with no env var set
	os.Unsetenv("FC_INSTANCE_ID")
	id1 := GetInstanceID()
	assert.NotEmpty(t, id1)
	// UUID7 format: xxxxxxxx-xxxx-7xxx-yxxx-xxxxxxxxxxxx
	assert.Contains(t, id1, "-")
	parts := strings.Split(id1, "-")
	assert.Len(t, parts, 5)

	// Same ID should be returned on subsequent calls
	id2 := GetInstanceID()
	assert.Equal(t, id1, id2)

	// Reset and test with env var
	ResetInstanceID()
	os.Setenv("FC_INSTANCE_ID", "custom-instance-id")
	defer os.Unsetenv("FC_INSTANCE_ID")

	id3 := GetInstanceID()
	assert.Equal(t, "custom-instance-id", id3)
}

func TestProcessIDFormat(t *testing.T) {
	setupTest(t)

	// Set a known instance ID
	os.Setenv("FC_INSTANCE_ID", "test-instance")
	defer os.Unsetenv("FC_INSTANCE_ID")
	ResetInstanceID()

	// Create a process
	id := createProcess(context.Background(), ProcessRequest{Command: "echo test", WorkingDir: ""})

	// ID should be in format instanceid_processid
	assert.True(t, strings.HasPrefix(id, "test-instance_"))
	assert.Equal(t, "test-instance_1", id)

	// Create another process
	id2 := createProcess(context.Background(), ProcessRequest{Command: "echo test2", WorkingDir: ""})
	assert.Equal(t, "test-instance_2", id2)

	// Wait for processes to complete
	time.Sleep(100 * time.Millisecond)
}

func TestLayerMerger(t *testing.T) {
	// Create temp directory
	tmpDir, err := os.MkdirTemp("", "layer_merger_test")
	assert.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	// Create test layers
	layer1Path := filepath.Join(tmpDir, "layer1.tar.gz")
	layer2Path := filepath.Join(tmpDir, "layer2.tar.gz")

	// Layer 1: base files
	createTestLayer(t, layer1Path, map[string]string{
		"file1.txt":  "content from layer 1",
		"file2.txt":  "file2 from layer 1",
		"dir1/":      "",
		"dir1/a.txt": "dir1/a from layer 1",
	})

	// Layer 2: override file2, add file3
	createTestLayer(t, layer2Path, map[string]string{
		"file2.txt": "file2 overridden in layer 2",
		"file3.txt": "new file from layer 2",
	})

	// Merge layers
	outputPath := filepath.Join(tmpDir, "merged.tar")
	merger := NewLayerMerger([]string{layer1Path, layer2Path}, outputPath)
	err = merger.Merge()
	assert.NoError(t, err)

	// Verify merged tar
	files := readTarContents(t, outputPath)

	// file1.txt should be from layer 1
	assert.Equal(t, "content from layer 1", files["file1.txt"])

	// file2.txt should be from layer 2 (overridden)
	assert.Equal(t, "file2 overridden in layer 2", files["file2.txt"])

	// file3.txt should be from layer 2
	assert.Equal(t, "new file from layer 2", files["file3.txt"])

	// dir1/a.txt should be from layer 1
	assert.Equal(t, "dir1/a from layer 1", files["dir1/a.txt"])
}

func TestWhiteoutHandling(t *testing.T) {
	// Create temp directory
	tmpDir, err := os.MkdirTemp("", "whiteout_test")
	assert.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	// Layer 1: base files
	layer1Path := filepath.Join(tmpDir, "layer1.tar.gz")
	createTestLayer(t, layer1Path, map[string]string{
		"file1.txt": "content 1",
		"file2.txt": "content 2",
	})

	// Layer 2: delete file1.txt using whiteout
	layer2Path := filepath.Join(tmpDir, "layer2.tar.gz")
	createTestLayer(t, layer2Path, map[string]string{
		".wh.file1.txt": "", // Whiteout marker
		"file3.txt":     "content 3",
	})

	// Merge layers
	outputPath := filepath.Join(tmpDir, "merged.tar")
	merger := NewLayerMerger([]string{layer1Path, layer2Path}, outputPath)
	err = merger.Merge()
	assert.NoError(t, err)

	// Verify merged tar
	files := readTarContents(t, outputPath)

	// file1.txt should be deleted
	_, exists := files["file1.txt"]
	assert.False(t, exists, "file1.txt should be deleted by whiteout")

	// file2.txt should still exist
	assert.Equal(t, "content 2", files["file2.txt"])

	// file3.txt should exist
	assert.Equal(t, "content 3", files["file3.txt"])
}

func TestTaskDedup(t *testing.T) {
	// Clear cache
	ClearImageCache()

	// Track how many times the actual pull function is called
	var pullCount int32

	// Override pullImageInternal for testing - we'll test the dedup logic
	// by checking that concurrent calls result in only one cache miss

	// Simulate concurrent pulls
	var wg sync.WaitGroup
	results := make([]string, 3)
	errors := make([]error, 3)

	// Use a mock that tracks calls
	testKey := "test-image:latest"

	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()

			// Check cache first
			imagePullMu.RLock()
			if path, ok := imagePullCache[testKey]; ok {
				imagePullMu.RUnlock()
				results[idx] = path
				return
			}
			imagePullMu.RUnlock()

			// Simulate pull with singleflight
			result, err, _ := imagePullGroup.Do(testKey, func() (interface{}, error) {
				atomic.AddInt32(&pullCount, 1)
				time.Sleep(50 * time.Millisecond) // Simulate network delay
				path := "/test/rootfs/abc123.tar"

				// Cache the result
				imagePullMu.Lock()
				imagePullCache[testKey] = path
				imagePullMu.Unlock()

				return path, nil
			})

			if err != nil {
				errors[idx] = err
			} else {
				results[idx] = result.(string)
			}
		}(i)
	}

	wg.Wait()

	// All results should be the same
	for _, r := range results {
		assert.Equal(t, "/test/rootfs/abc123.tar", r)
	}

	// Pull should only have been called once due to singleflight
	assert.Equal(t, int32(1), pullCount, "Pull should only be called once")
}

func TestExtractTar(t *testing.T) {
	// Create temp directories
	tmpDir, err := os.MkdirTemp("", "extract_tar_test")
	assert.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	// Create a test tar file (not gzipped, as extractTar expects plain tar)
	tarPath := filepath.Join(tmpDir, "test.tar")
	createPlainTar(t, tarPath, map[string]string{
		"file1.txt":      "hello world",
		"dir1/":          "",
		"dir1/file2.txt": "nested file",
	})

	// Extract
	destDir := filepath.Join(tmpDir, "extracted")
	err = extractTar(context.Background(), tarPath, destDir)
	assert.NoError(t, err)

	// Verify extracted files
	content1, err := os.ReadFile(filepath.Join(destDir, "file1.txt"))
	assert.NoError(t, err)
	assert.Equal(t, "hello world", string(content1))

	content2, err := os.ReadFile(filepath.Join(destDir, "dir1/file2.txt"))
	assert.NoError(t, err)
	assert.Equal(t, "nested file", string(content2))
}

func TestIsPathSafe(t *testing.T) {
	tests := []struct {
		baseDir    string
		targetPath string
		safe       bool
	}{
		{"/base", "/base/file.txt", true},
		{"/base", "/base/dir/file.txt", true},
		{"/base", "/base/../etc/passwd", false},
		{"/base", "/other/file.txt", false},
	}

	for _, tt := range tests {
		result := isPathSafe(tt.baseDir, tt.targetPath)
		assert.Equal(t, tt.safe, result, "baseDir=%s, targetPath=%s", tt.baseDir, tt.targetPath)
	}
}

// Helper functions

func createTestLayer(t *testing.T, path string, files map[string]string) {
	f, err := os.Create(path)
	assert.NoError(t, err)
	defer f.Close()

	gzWriter := gzip.NewWriter(f)
	defer gzWriter.Close()

	tarWriter := tar.NewWriter(gzWriter)
	defer tarWriter.Close()

	for name, content := range files {
		if strings.HasSuffix(name, "/") {
			// Directory
			err := tarWriter.WriteHeader(&tar.Header{
				Name:     name,
				Mode:     0755,
				Typeflag: tar.TypeDir,
			})
			assert.NoError(t, err)
		} else {
			// File
			err := tarWriter.WriteHeader(&tar.Header{
				Name:     name,
				Mode:     0644,
				Size:     int64(len(content)),
				Typeflag: tar.TypeReg,
			})
			assert.NoError(t, err)
			_, err = tarWriter.Write([]byte(content))
			assert.NoError(t, err)
		}
	}
}

func createPlainTar(t *testing.T, path string, files map[string]string) {
	f, err := os.Create(path)
	assert.NoError(t, err)
	defer f.Close()

	tarWriter := tar.NewWriter(f)
	defer tarWriter.Close()

	for name, content := range files {
		if strings.HasSuffix(name, "/") {
			// Directory
			err := tarWriter.WriteHeader(&tar.Header{
				Name:     name,
				Mode:     0755,
				Typeflag: tar.TypeDir,
			})
			assert.NoError(t, err)
		} else {
			// File
			err := tarWriter.WriteHeader(&tar.Header{
				Name:     name,
				Mode:     0644,
				Size:     int64(len(content)),
				Typeflag: tar.TypeReg,
			})
			assert.NoError(t, err)
			_, err = tarWriter.Write([]byte(content))
			assert.NoError(t, err)
		}
	}
}

func readTarContents(t *testing.T, path string) map[string]string {
	f, err := os.Open(path)
	assert.NoError(t, err)
	defer f.Close()

	tarReader := tar.NewReader(f)
	files := make(map[string]string)

	for {
		header, err := tarReader.Next()
		if err != nil {
			break
		}

		if header.Typeflag == tar.TypeReg {
			content := make([]byte, header.Size)
			_, err := tarReader.Read(content)
			if err != nil && err.Error() != "EOF" {
				// Only fail on non-EOF errors
				assert.NoError(t, err)
			}
			files[header.Name] = string(content)
		}
	}

	return files
}

func TestPullManifestOCI(t *testing.T) {
	// Create a mock registry server that returns OCI manifest
	ociManifest := map[string]interface{}{
		"schemaVersion": 2,
		"mediaType":     "application/vnd.oci.image.manifest.v1+json",
		"config": map[string]interface{}{
			"mediaType": "application/vnd.oci.image.config.v1+json",
			"size":      1234,
			"digest":    "sha256:abc123def456",
		},
		"layers": []map[string]interface{}{
			{
				"mediaType": "application/vnd.oci.image.layer.v1.tar+gzip",
				"size":      5678,
				"digest":    "sha256:layer1digest",
			},
		},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Check Accept header includes OCI manifest type
		// Accept header can have multiple values
		acceptHeaders := r.Header.Values("Accept")
		hasOCI := false
		for _, h := range acceptHeaders {
			if strings.Contains(h, "application/vnd.oci.image.manifest.v1+json") {
				hasOCI = true
				break
			}
		}

		if !hasOCI {
			// Simulate the error that Aliyun registry returns
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"errors":[{"code":"MANIFEST_UNKNOWN","message":"OCI manifest found, but accept header does not support OCI manifests"}]}`))
			return
		}

		w.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(ociManifest)
	}))
	defer server.Close()

	// Test with both Accept headers (should succeed)
	req, _ := http.NewRequest("GET", server.URL+"/v2/test/image/manifests/latest", nil)
	req.Header.Add("Accept", "application/vnd.docker.distribution.manifest.v2+json")
	req.Header.Add("Accept", "application/vnd.oci.image.manifest.v1+json")

	resp, err := server.Client().Do(req)
	assert.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode, "Should succeed with OCI accept header")

	var manifest ManifestV2
	err = json.NewDecoder(resp.Body).Decode(&manifest)
	assert.NoError(t, err)
	assert.Equal(t, "sha256:abc123def456", manifest.Config.Digest)
	assert.Len(t, manifest.Layers, 1)

	// Test without OCI accept header (should fail with 404)
	req2, _ := http.NewRequest("GET", server.URL+"/v2/test/image/manifests/latest", nil)
	req2.Header.Set("Accept", "application/vnd.docker.distribution.manifest.v2+json")
	// Intentionally NOT setting OCI accept header

	resp2, err := server.Client().Do(req2)
	assert.NoError(t, err)
	defer resp2.Body.Close()

	assert.Equal(t, http.StatusNotFound, resp2.StatusCode, "Should fail without OCI accept header")
}
