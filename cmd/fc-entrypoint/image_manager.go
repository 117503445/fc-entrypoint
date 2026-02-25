package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/rs/zerolog/log"
)

// PrepareImage prepares an image for use, returning the path to the rootfs tar
// This function handles caching and deduplication of image pulls
func PrepareImage(ctx context.Context, image, username, password string) (string, error) {
	return PullImageWithDedup(ctx, image, username, password)
}

// pullImageInternal actually pulls an image and creates the rootfs tar
// This is called by PullImageWithDedup
func pullImageInternal(ctx context.Context, image, username, password string) (string, error) {
	// Parse image reference
	info, err := ParseImageReference(image)
	if err != nil {
		return "", fmt.Errorf("failed to parse image reference: %w", err)
	}

	log.Ctx(ctx).Info().
		Str("registry", info.Registry).
		Str("repository", info.Repository).
		Str("tag", info.Tag).
		Msg("Pulling image")

	// Create registry client
	client := NewRegistryClient(ctx, username, password)

	// Pull manifest
	if err := client.PullManifest(info); err != nil {
		return "", fmt.Errorf("failed to pull manifest: %w", err)
	}

	// Check if rootfs already exists
	digest := GetImageDigest(info)
	rootfsPath := filepath.Join(getDirData(), "rootfs", digest+".tar")
	if _, err := os.Stat(rootfsPath); err == nil {
		log.Ctx(ctx).Info().Str("path", rootfsPath).Msg("Rootfs already exists, skipping layer download and merge")
		return rootfsPath, nil
	}

	// Download layers
	imagesDir := filepath.Join(getDirData(), "images", digest)
	layerPaths := make([]string, len(info.Layers))

	for i := range info.Layers {
		layer := &info.Layers[i]
		if err := client.DownloadLayer(info, layer, imagesDir); err != nil {
			return "", fmt.Errorf("failed to download layer %d: %w", i, err)
		}
		layerPaths[i] = layer.LocalPath
	}

	// Merge layers into rootfs
	if err := os.MkdirAll(filepath.Dir(rootfsPath), 0755); err != nil {
		return "", fmt.Errorf("failed to create rootfs directory: %w", err)
	}

	merger := NewLayerMerger(layerPaths, rootfsPath)
	if err := merger.Merge(); err != nil {
		return "", fmt.Errorf("failed to merge layers: %w", err)
	}

	log.Ctx(ctx).Info().
		Str("image", image).
		Str("rootfs", rootfsPath).
		Msg("Image prepared successfully")

	return rootfsPath, nil
}