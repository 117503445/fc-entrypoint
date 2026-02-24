package main

import (
	"sync"

	"golang.org/x/sync/singleflight"
)

var (
	imagePullGroup singleflight.Group
	imagePullMu    sync.RWMutex
	imagePullCache = make(map[string]string) // image -> rootfs path
)

// PullImageWithDedup pulls an image with deduplication
// If multiple goroutines request the same image, only one will actually pull
func PullImageWithDedup(image, username, password string) (string, error) {
	// First check cache
	imagePullMu.RLock()
	if path, ok := imagePullCache[image]; ok {
		imagePullMu.RUnlock()
		return path, nil
	}
	imagePullMu.RUnlock()

	// Use singleflight to ensure only one pull per image
	result, err, _ := imagePullGroup.Do(image, func() (interface{}, error) {
		// Check cache again inside singleflight
		imagePullMu.RLock()
		if path, ok := imagePullCache[image]; ok {
			imagePullMu.RUnlock()
			return path, nil
		}
		imagePullMu.RUnlock()

		// Actually pull the image
		path, err := pullImageInternal(image, username, password)
		if err != nil {
			return "", err
		}

		// Cache the result
		imagePullMu.Lock()
		imagePullCache[image] = path
		imagePullMu.Unlock()

		return path, nil
	})

	if err != nil {
		return "", err
	}
	return result.(string), nil
}

// ClearImageCache clears the image pull cache (for testing)
func ClearImageCache() {
	imagePullMu.Lock()
	imagePullCache = make(map[string]string)
	imagePullMu.Unlock()
}
