package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/rs/zerolog/log"
)

const (
	whiteoutPrefix  = ".wh."
	opaqueWhiteout  = ".wh..wh..opq"
	mergeBufferSize = 64 * 1024 // 64KB buffer
)

// FileEntry represents a file entry in the merged filesystem
type FileEntry struct {
	LayerIndex int // Which layer this file comes from
	Header     *tar.Header
	Deleted    bool // Whether this file is marked for deletion (whiteout)
}

// LayerMerger merges multiple tar.gz layers into a single tar file
type LayerMerger struct {
	ctx        context.Context
	layers     []string // layer file paths (from bottom to top)
	outputPath string
}

// NewLayerMerger creates a new layer merger
func NewLayerMerger(layers []string, outputPath string) *LayerMerger {
	return &LayerMerger{
		ctx:        context.Background(),
		layers:     layers,
		outputPath: outputPath,
	}
}

// Merge merges all layers into a single tar file
func (m *LayerMerger) Merge() error {
	// Phase 1: Build file index
	fileIndex, opaqueDirectories, err := m.buildFileIndex()
	if err != nil {
		return fmt.Errorf("failed to build file index: %w", err)
	}

	log.Ctx(m.ctx).Info().Int("files", len(fileIndex)).Int("layers", len(m.layers)).Msg("Built file index")

	// Phase 2: Stream merge
	if err := m.streamMerge(fileIndex, opaqueDirectories); err != nil {
		return fmt.Errorf("failed to merge layers: %w", err)
	}

	log.Ctx(m.ctx).Info().Str("output", m.outputPath).Msg("Layer merge completed")
	return nil
}

// buildFileIndex scans all layers and builds an index of files
// Returns: fileIndex (path -> FileEntry), opaqueDirectories (set of directories marked opaque)
func (m *LayerMerger) buildFileIndex() (map[string]*FileEntry, map[string]int, error) {
	fileIndex := make(map[string]*FileEntry)
	opaqueDirectories := make(map[string]int) // directory -> layer index where it became opaque

	for layerIdx, layerPath := range m.layers {
		if err := m.scanLayer(layerPath, layerIdx, fileIndex, opaqueDirectories); err != nil {
			return nil, nil, fmt.Errorf("failed to scan layer %d: %w", layerIdx, err)
		}
	}

	return fileIndex, opaqueDirectories, nil
}

// scanLayer scans a single layer and updates the file index
func (m *LayerMerger) scanLayer(layerPath string, layerIdx int, fileIndex map[string]*FileEntry, opaqueDirectories map[string]int) error {
	file, err := os.Open(layerPath)
	if err != nil {
		return err
	}
	defer file.Close()

	gzReader, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer gzReader.Close()

	tarReader := tar.NewReader(gzReader)

	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		name := cleanPath(header.Name)
		if name == "" {
			continue
		}

		baseName := filepath.Base(name)
		dirName := filepath.Dir(name)

		// Check for opaque whiteout (marks directory to hide all previous content)
		if baseName == opaqueWhiteout {
			opaqueDirectories[dirName] = layerIdx
			continue
		}

		// Check for whiteout file (marks file/dir for deletion)
		if strings.HasPrefix(baseName, whiteoutPrefix) {
			targetName := strings.TrimPrefix(baseName, whiteoutPrefix)
			targetPath := filepath.Join(dirName, targetName)
			if targetPath != "." {
				fileIndex[targetPath] = &FileEntry{
					LayerIndex: layerIdx,
					Deleted:    true,
				}
			}
			continue
		}

		// Regular file - record it (later layers override earlier ones)
		headerCopy := *header
		headerCopy.Name = name
		fileIndex[name] = &FileEntry{
			LayerIndex: layerIdx,
			Header:     &headerCopy,
			Deleted:    false,
		}
	}

	return nil
}

// streamMerge creates the final tar by streaming from each layer
func (m *LayerMerger) streamMerge(fileIndex map[string]*FileEntry, opaqueDirectories map[string]int) error {
	// Create output directory
	if err := os.MkdirAll(filepath.Dir(m.outputPath), 0755); err != nil {
		return err
	}

	// Create output tar file
	tmpPath := m.outputPath + ".tmp"
	outFile, err := os.Create(tmpPath)
	if err != nil {
		return err
	}

	tarWriter := tar.NewWriter(outFile)

	// Track which files we've written
	writtenFiles := make(map[string]bool)

	// Process layers from bottom to top
	for layerIdx, layerPath := range m.layers {
		if err := m.writeLayerFiles(layerPath, layerIdx, fileIndex, opaqueDirectories, tarWriter, writtenFiles); err != nil {
			tarWriter.Close()
			outFile.Close()
			os.Remove(tmpPath)
			return fmt.Errorf("failed to write layer %d: %w", layerIdx, err)
		}
	}

	if err := tarWriter.Close(); err != nil {
		outFile.Close()
		os.Remove(tmpPath)
		return err
	}

	if err := outFile.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}

	// Rename to final path
	return os.Rename(tmpPath, m.outputPath)
}

// writeLayerFiles writes files from a layer that belong to it according to the index
func (m *LayerMerger) writeLayerFiles(layerPath string, layerIdx int, fileIndex map[string]*FileEntry, opaqueDirectories map[string]int, tarWriter *tar.Writer, writtenFiles map[string]bool) error {
	file, err := os.Open(layerPath)
	if err != nil {
		return err
	}
	defer file.Close()

	gzReader, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer gzReader.Close()

	tarReader := tar.NewReader(gzReader)
	buf := make([]byte, mergeBufferSize)

	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		name := cleanPath(header.Name)
		if name == "" {
			continue
		}

		baseName := filepath.Base(name)

		// Skip whiteout files
		if baseName == opaqueWhiteout || strings.HasPrefix(baseName, whiteoutPrefix) {
			// Still need to read the content to advance the reader
			if header.Size > 0 {
				io.CopyN(io.Discard, tarReader, header.Size)
			}
			continue
		}

		// Check if this file should be written from this layer
		entry, exists := fileIndex[name]
		if !exists || entry.Deleted || entry.LayerIndex != layerIdx {
			// Skip this file - either doesn't exist, is deleted, or belongs to another layer
			if header.Size > 0 {
				io.CopyN(io.Discard, tarReader, header.Size)
			}
			continue
		}

		// Check if file is under an opaque directory from a later layer
		if m.isUnderOpaqueDirectory(name, layerIdx, opaqueDirectories) {
			if header.Size > 0 {
				io.CopyN(io.Discard, tarReader, header.Size)
			}
			continue
		}

		// Skip if already written
		if writtenFiles[name] {
			if header.Size > 0 {
				io.CopyN(io.Discard, tarReader, header.Size)
			}
			continue
		}

		// Write header
		header.Name = name
		if err := tarWriter.WriteHeader(header); err != nil {
			return err
		}

		// Write content (if any)
		if header.Size > 0 {
			if _, err := io.CopyBuffer(tarWriter, tarReader, buf); err != nil {
				return err
			}
		}

		writtenFiles[name] = true
	}

	return nil
}

// isUnderOpaqueDirectory checks if a path is under a directory that became opaque in a later layer
func (m *LayerMerger) isUnderOpaqueDirectory(path string, layerIdx int, opaqueDirectories map[string]int) bool {
	dir := filepath.Dir(path)
	for dir != "." && dir != "/" {
		if opaqueLayerIdx, ok := opaqueDirectories[dir]; ok && opaqueLayerIdx > layerIdx {
			return true
		}
		dir = filepath.Dir(dir)
	}
	return false
}

// cleanPath normalizes a path
func cleanPath(path string) string {
	// Remove leading ./
	path = strings.TrimPrefix(path, "./")
	// Remove leading /
	path = strings.TrimPrefix(path, "/")
	// Clean the path
	path = filepath.Clean(path)
	// Skip . and empty paths
	if path == "." || path == "" {
		return ""
	}
	return path
}