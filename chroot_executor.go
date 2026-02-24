package main

import (
	"archive/tar"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"github.com/rs/zerolog/log"
)

const extractBufferSize = 64 * 1024 // 64KB buffer

// ExecuteInChroot executes a command in a chroot environment
func ExecuteInChroot(rootfsTar, processDir, command, workingDir string, stdout, stderr io.Writer) error {
	// Extract rootfs to process directory
	if err := extractTar(rootfsTar, processDir); err != nil {
		return fmt.Errorf("failed to extract rootfs: %w", err)
	}

	log.Info().Str("rootfs", processDir).Str("command", command).Msg("Executing in chroot")

	// Check if we have root privileges
	if os.Geteuid() != 0 {
		return fmt.Errorf("chroot requires root privileges (current euid: %d)", os.Geteuid())
	}

	// Determine working directory inside chroot
	chrootWorkDir := "/"
	if workingDir != "" {
		chrootWorkDir = workingDir
	}

	// Execute command in chroot
	cmd := exec.Command("/bin/sh", "-c", command)
	cmd.Dir = chrootWorkDir
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Chroot: processDir,
	}

	err := cmd.Run()
	if err != nil {
		return fmt.Errorf("command execution failed: %w", err)
	}

	return nil
}

// extractTar extracts a tar file to a directory
func extractTar(tarPath, destDir string) error {
	// Create destination directory
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	file, err := os.Open(tarPath)
	if err != nil {
		return fmt.Errorf("failed to open tar: %w", err)
	}
	defer file.Close()

	tarReader := tar.NewReader(file)
	buf := make([]byte, extractBufferSize)

	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("failed to read tar header: %w", err)
		}

		targetPath := filepath.Join(destDir, header.Name)

		// Security check: ensure path doesn't escape destDir
		if !isPathSafe(destDir, targetPath) {
			log.Warn().Str("path", header.Name).Msg("Skipping potentially unsafe path")
			continue
		}

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(targetPath, os.FileMode(header.Mode)); err != nil {
				return fmt.Errorf("failed to create directory %s: %w", targetPath, err)
			}

		case tar.TypeReg:
			// Ensure parent directory exists
			parentDir := filepath.Dir(targetPath)
			if err := os.MkdirAll(parentDir, 0755); err != nil {
				return fmt.Errorf("failed to create parent directory: %w", err)
			}

			outFile, err := os.OpenFile(targetPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(header.Mode))
			if err != nil {
				return fmt.Errorf("failed to create file %s: %w", targetPath, err)
			}

			if _, err := io.CopyBuffer(outFile, tarReader, buf); err != nil {
				outFile.Close()
				return fmt.Errorf("failed to write file %s: %w", targetPath, err)
			}
			outFile.Close()

		case tar.TypeSymlink:
			// Ensure parent directory exists
			parentDir := filepath.Dir(targetPath)
			if err := os.MkdirAll(parentDir, 0755); err != nil {
				return fmt.Errorf("failed to create parent directory: %w", err)
			}

			// Remove existing file/symlink if exists
			os.Remove(targetPath)

			if err := os.Symlink(header.Linkname, targetPath); err != nil {
				return fmt.Errorf("failed to create symlink %s: %w", targetPath, err)
			}

		case tar.TypeLink:
			// Ensure parent directory exists
			parentDir := filepath.Dir(targetPath)
			if err := os.MkdirAll(parentDir, 0755); err != nil {
				return fmt.Errorf("failed to create parent directory: %w", err)
			}

			// Hard link
			linkTarget := filepath.Join(destDir, header.Linkname)
			os.Remove(targetPath)
			if err := os.Link(linkTarget, targetPath); err != nil {
				// If hard link fails, try to copy the file
				log.Warn().Str("target", targetPath).Err(err).Msg("Hard link failed, will skip")
			}

		case tar.TypeChar, tar.TypeBlock:
			// Skip device files - they require root and are typically not needed
			log.Debug().Str("path", header.Name).Msg("Skipping device file")

		case tar.TypeFifo:
			// Skip FIFO files
			log.Debug().Str("path", header.Name).Msg("Skipping FIFO file")

		default:
			log.Debug().
				Str("path", header.Name).
				Int("type", int(header.Typeflag)).
				Msg("Skipping unknown file type")
		}
	}

	log.Info().Str("path", destDir).Msg("Extracted rootfs")
	return nil
}

// isPathSafe checks if a path is safe (doesn't escape the base directory)
func isPathSafe(baseDir, targetPath string) bool {
	// Clean and resolve paths
	absBase, err := filepath.Abs(baseDir)
	if err != nil {
		return false
	}

	absTarget, err := filepath.Abs(targetPath)
	if err != nil {
		return false
	}

	// Check if target is under base
	rel, err := filepath.Rel(absBase, absTarget)
	if err != nil {
		return false
	}

	// Path is unsafe if it starts with ".."
	return !filepath.IsAbs(rel) && rel != ".." && !startsWithDoubleDot(rel)
}

func startsWithDoubleDot(path string) bool {
	if len(path) < 2 {
		return false
	}
	return path[0] == '.' && path[1] == '.' && (len(path) == 2 || path[2] == filepath.Separator)
}
