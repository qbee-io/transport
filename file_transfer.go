// Copyright 2026 qbee.io
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//
// SPDX-License-Identifier: Apache-2.0

package transport

import (
	"archive/tar"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/xtaci/smux"
)

// FileTransferDirection defines the direction of a file transfer.
type FileTransferDirection uint8

const (
	// FileTransferDownload transfers files from device to client.
	FileTransferDownload FileTransferDirection = 0

	// FileTransferUpload transfers files from client to device.
	FileTransferUpload FileTransferDirection = 1
)

// FileTransferRequest is the JSON payload for a file transfer handshake.
type FileTransferRequest struct {
	// Direction specifies whether the transfer is an upload or download.
	Direction FileTransferDirection `json:"direction"`

	// Path is the absolute path on the device.
	// For downloads, this is the source path to archive.
	// For uploads, this is the destination directory to extract into.
	Path string `json:"path"`
}

// DownloadFile downloads a file or directory from the device to localDestPath.
// remotePath must be an absolute path on the device.
// localDestPath is the local directory to extract the tar archive into.
func (cli *Client) DownloadFile(ctx context.Context, remotePath, localDestPath string) error {
	req := FileTransferRequest{
		Direction: FileTransferDownload,
		Path:      remotePath,
	}

	payload, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("error marshaling file transfer request: %w", err)
	}

	stream, err := cli.OpenStream(ctx, MessageTypeFile, payload)
	if err != nil {
		return err
	}
	defer func() {
		_ = stream.Close()
	}()

	tr := tar.NewReader(stream)
	return extractTar(tr, localDestPath)
}

// UploadFile uploads a local file or directory to the device.
// localPath is the local file or directory to archive and send.
// remoteDestPath must be an absolute path on the device where files will be extracted.
func (cli *Client) UploadFile(ctx context.Context, localPath, remoteDestPath string) error {
	req := FileTransferRequest{
		Direction: FileTransferUpload,
		Path:      remoteDestPath,
	}

	payload, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("error marshaling file transfer request: %w", err)
	}

	stream, err := cli.OpenStream(ctx, MessageTypeFile, payload)
	if err != nil {
		return err
	}
	defer func() {
		_ = stream.Close()
	}()

	tw := tar.NewWriter(stream)

	if err = archivePath(tw, localPath); err != nil {
		return err
	}

	// Close the tar writer to flush end-of-archive markers.
	// The device will detect the end of the tar stream and send a final response.
	if err = tw.Close(); err != nil {
		return err
	}

	// Wait for the device to confirm extraction completed.
	if _, err = ExpectOK(stream); err != nil {
		return fmt.Errorf("device extraction failed: %w", err)
	}

	return nil
}

// HandleFileTransfer handles a file transfer request on the device side.
func HandleFileTransfer(ctx context.Context, stream *smux.Stream, payload []byte) error {
	var req FileTransferRequest

	if err := json.Unmarshal(payload, &req); err != nil {
		return WriteError(stream, fmt.Errorf("invalid file transfer request: %w", err))
	}

	if !filepath.IsAbs(req.Path) {
		return WriteError(stream, fmt.Errorf("path must be absolute"))
	}

	switch req.Direction {
	case FileTransferDownload:
		return handleDownload(stream, req.Path)
	case FileTransferUpload:
		return handleUpload(stream, req.Path)
	default:
		return WriteError(stream, fmt.Errorf("unsupported direction: %d", req.Direction))
	}
}

func handleDownload(stream *smux.Stream, path string) error {
	if _, err := os.Lstat(path); err != nil {
		return WriteError(stream, err)
	}

	if err := WriteOK(stream, nil); err != nil {
		return err
	}

	tw := tar.NewWriter(stream)

	if err := archivePath(tw, path); err != nil {
		return err
	}

	return tw.Close()
}

func handleUpload(stream *smux.Stream, destPath string) error {
	info, err := os.Stat(destPath)
	if err != nil {
		return WriteError(stream, err)
	}

	if !info.IsDir() {
		return WriteError(stream, fmt.Errorf("upload destination must be a directory"))
	}

	if err = WriteOK(stream, nil); err != nil {
		return err
	}

	tr := tar.NewReader(stream)

	if extractErr := extractTar(tr, destPath); extractErr != nil {
		return WriteError(stream, extractErr)
	}

	return WriteOK(stream, nil)
}

// archivePath writes the file or directory at basePath to the tar writer.
// For a single file, the archive contains one entry with the file's base name.
// For a directory, the archive contains the directory and all its contents,
// preserving the top-level directory name.
// Symlinks pointing outside basePath are silently skipped.
func archivePath(tw *tar.Writer, basePath string) error {
	basePath = filepath.Clean(basePath)

	info, err := os.Lstat(basePath)
	if err != nil {
		return fmt.Errorf("error stat %s: %w", basePath, err)
	}

	if !info.IsDir() {
		return archiveFile(tw, basePath, info.Name(), info)
	}

	// filepath.Walk uses os.Lstat internally, so info has the symlink bit set.
	return filepath.Walk(basePath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Compute relative path preserving the top-level directory name.
		relPath, err := filepath.Rel(filepath.Dir(basePath), path)
		if err != nil {
			return fmt.Errorf("error computing relative path for %s: %w", path, err)
		}

		relPath = filepath.ToSlash(relPath)

		switch {
		case info.Mode()&os.ModeSymlink != 0:
			return archiveSymlink(tw, basePath, path, relPath)
		case info.IsDir():
			return archiveDir(tw, relPath, info)
		case info.Mode().IsRegular():
			return archiveFile(tw, path, relPath, info)
		default:
			// Skip special files (devices, sockets, etc.)
			return nil
		}
	})
}

func archiveDir(tw *tar.Writer, relPath string, info os.FileInfo) error {
	header, err := tar.FileInfoHeader(info, "")
	if err != nil {
		return fmt.Errorf("error creating tar header for directory %s: %w", relPath, err)
	}

	header.Name = relPath + "/"

	return tw.WriteHeader(header)
}

func archiveFile(tw *tar.Writer, absPath, relPath string, info os.FileInfo) error {
	header, err := tar.FileInfoHeader(info, "")
	if err != nil {
		return fmt.Errorf("error creating tar header for %s: %w", relPath, err)
	}

	header.Name = relPath

	if err = tw.WriteHeader(header); err != nil {
		return fmt.Errorf("error writing tar header for %s: %w", relPath, err)
	}

	f, err := os.Open(absPath)
	if err != nil {
		return fmt.Errorf("error opening %s: %w", absPath, err)
	}
	defer func() {
		_ = f.Close()
	}()

	if _, err = io.Copy(tw, f); err != nil {
		return fmt.Errorf("error writing %s to tar: %w", relPath, err)
	}

	return nil
}

func archiveSymlink(tw *tar.Writer, basePath, absPath, relPath string) error {
	linkTarget, err := os.Readlink(absPath)
	if err != nil {
		return fmt.Errorf("error reading symlink %s: %w", absPath, err)
	}

	// Validate the symlink target stays within the base path.
	if err = validateSymlink(basePath, linkTarget, absPath); err != nil {
		// Skip symlinks that escape the base directory.
		return nil
	}

	// Convert absolute targets to relative paths for archive portability.
	if filepath.IsAbs(linkTarget) {
		linkTarget, err = filepath.Rel(filepath.Dir(absPath), linkTarget)
		if err != nil {
			// Skip if we can't compute a relative path.
			return nil
		}
	}

	header := &tar.Header{
		Typeflag: tar.TypeSymlink,
		Name:     relPath,
		Linkname: filepath.ToSlash(linkTarget),
	}

	return tw.WriteHeader(header)
}

// extractTar reads a tar archive and extracts its contents to destPath.
// All paths are validated to prevent directory traversal attacks.
// Symlinks with targets outside destPath are rejected.
func extractTar(tr *tar.Reader, destPath string) error {
	destPath = filepath.Clean(destPath)

	for {
		header, err := tr.Next()
		if err == io.EOF {
			return nil
		}

		if err != nil {
			return fmt.Errorf("error reading tar: %w", err)
		}

		targetPath, err := validatePath(destPath, header.Name)
		if err != nil {
			return err
		}

		switch header.Typeflag {
		case tar.TypeDir:
			if err = os.MkdirAll(targetPath, os.FileMode(header.Mode)); err != nil {
				return fmt.Errorf("error creating directory %s: %w", targetPath, err)
			}

		case tar.TypeReg:
			if err = extractFile(tr, targetPath, header); err != nil {
				return err
			}

		case tar.TypeSymlink:
			if err = extractSymlink(destPath, targetPath, header); err != nil {
				return err
			}

		default:
			// Skip unsupported entry types.
			continue
		}
	}
}

func extractFile(tr *tar.Reader, targetPath string, header *tar.Header) error {
	if err := os.MkdirAll(filepath.Dir(targetPath), 0755); err != nil {
		return fmt.Errorf("error creating parent directory for %s: %w", targetPath, err)
	}

	f, err := os.OpenFile(targetPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(header.Mode))
	if err != nil {
		return fmt.Errorf("error creating file %s: %w", targetPath, err)
	}
	defer func() {
		_ = f.Close()
	}()

	if _, err = io.Copy(f, io.LimitReader(tr, header.Size)); err != nil {
		return fmt.Errorf("error extracting file %s: %w", targetPath, err)
	}

	return nil
}

func extractSymlink(destPath, targetPath string, header *tar.Header) error {
	if err := validateSymlink(destPath, header.Linkname, targetPath); err != nil {
		return err
	}

	// Remove existing file/symlink at target path before creating new symlink.
	_ = os.Remove(targetPath)

	if err := os.Symlink(header.Linkname, targetPath); err != nil {
		return fmt.Errorf("error creating symlink %s: %w", targetPath, err)
	}

	return nil
}

// validatePath checks that the given tar entry name resolves to a path
// within the destination directory. Returns the cleaned absolute path.
func validatePath(basePath, entryName string) (string, error) {
	cleanBase := filepath.Clean(basePath)
	fullPath := filepath.Join(cleanBase, filepath.FromSlash(entryName))
	cleanPath := filepath.Clean(fullPath)

	// The path must be the base itself or a child of it.
	if cleanPath == cleanBase {
		return cleanPath, nil
	}

	if !strings.HasPrefix(cleanPath, cleanBase+string(filepath.Separator)) {
		return "", fmt.Errorf("path traversal detected: %s", entryName)
	}

	return cleanPath, nil
}

// validateSymlink checks that a symlink target resolves to a path
// within the base directory.
func validateSymlink(basePath, linkTarget, linkPath string) error {
	cleanBase := filepath.Clean(basePath)

	var resolved string

	linkDir := filepath.Dir(linkPath)

	if filepath.IsAbs(linkTarget) {
		resolved = filepath.Clean(linkTarget)
	} else {
		resolved = filepath.Clean(filepath.Join(linkDir, linkTarget))
	}

	if resolved == cleanBase || strings.HasPrefix(resolved, cleanBase+string(filepath.Separator)) {
		return nil
	}

	return fmt.Errorf("symlink %s target %s escapes base directory %s", linkPath, linkTarget, basePath)
}
