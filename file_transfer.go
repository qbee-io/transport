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
	"compress/gzip"
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

// ErrFileTransfer represents an error that occurred during file transfer operations.
type ErrFileTransfer struct {
	Message string
}

// Error implements the error interface for ErrFileTransfer.
func (e ErrFileTransfer) Error() string {
	return e.Message
}

func fileTransferErrorfromRemoteError(err error) error {
	return ErrFileTransfer{
		Message: strings.TrimPrefix(err.Error(), "error reading message: remote error: "),
	}
}

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
		return fileTransferErrorfromRemoteError(err)
	}
	defer func() {
		_ = stream.Close()
	}()

	gzipReader, err := gzip.NewReader(stream)
	if err != nil {
		return fmt.Errorf("error creating gzip reader: %w", err)
	}

	gzipReader.Multistream(false)

	tarReader := tar.NewReader(gzipReader)

	if err = extractTar(tarReader, localDestPath); err != nil {
		_ = gzipReader.Close()
		return fmt.Errorf("error extracting tar: %w", err)
	}

	return gzipReader.Close()
}

// UploadFile uploads a local file or directory to the device.
// localPath is the local file or directory to archive and send.
// remoteDestPath must be an absolute path on the device where files will be extracted.
func (cli *Client) UploadFile(ctx context.Context, localPath, remoteDestPath string) error {
	// Validate that the source path exists before attempting to upload
	if _, err := os.Lstat(localPath); err != nil {
		if os.IsNotExist(err) {
			return ErrFileTransfer{Message: "Invalid source - path does not exist"}
		}
		return err
	}

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
		return fileTransferErrorfromRemoteError(err)
	}
	defer func() {
		_ = stream.Close()
	}()

	gzipWriter := gzip.NewWriter(stream)

	tarWriter := tar.NewWriter(gzipWriter)

	if err = archivePath(tarWriter, localPath); err != nil {
		return err
	}

	// Close the tar writer to flush end-of-archive markers.
	// The device will detect the end of the tar stream and send a final response.
	if err = tarWriter.Close(); err != nil {
		return err
	}

	if err = gzipWriter.Close(); err != nil {
		return fmt.Errorf("error closing gzip writer: %w", err)
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
		if os.IsNotExist(err) {
			return WriteError(stream, ErrFileTransfer{Message: "Invalid source - path does not exist"})
		}
		return WriteError(stream, err)
	}

	if err := WriteOK(stream, nil); err != nil {
		return err
	}

	gzipWriter := gzip.NewWriter(stream)

	tarWriter := tar.NewWriter(gzipWriter)

	if err := archivePath(tarWriter, path); err != nil {
		return err
	}

	if err := tarWriter.Close(); err != nil {
		return err
	}

	return gzipWriter.Close()
}

// validateParentDir validates that the parent directory of a path exists and is a directory.
func validateParentDir(targetPath string) error {
	parentDir := filepath.Dir(targetPath)
	parentInfo, err := os.Lstat(parentDir)
	if err != nil {
		if os.IsNotExist(err) {
			return ErrFileTransfer{Message: "Invalid destination - parent directory does not exist"}
		}
		return err
	}

	if !parentInfo.IsDir() {
		return ErrFileTransfer{Message: "Invalid destination - parent not a directory"}
	}

	return nil
}

func validateUploadDest(destPath string) error {
	// Destination path must be absolute to prevent confusion about where files will be extracted.
	if !filepath.IsAbs(destPath) {
		return ErrFileTransfer{Message: "Invalid destination - path must be absolute"}
	}

	// Check whether the parent directory of the destination exists and is a directory.
	return validateParentDir(destPath)
}

func handleUpload(stream *smux.Stream, destPath string) error {
	if err := validateUploadDest(destPath); err != nil {
		return WriteError(stream, err)
	}

	if err := WriteOK(stream, nil); err != nil {
		return err
	}

	gzipReader, err := gzip.NewReader(stream)
	if err != nil {
		return fmt.Errorf("error creating gzip reader: %w", err)
	}

	// Disable multistream so the reader returns io.EOF after the first gzip member
	// instead of blocking while trying to read the next member from the still-open stream.
	gzipReader.Multistream(false)

	tarReader := tar.NewReader(gzipReader)

	if err := extractTar(tarReader, destPath); err != nil {
		return err
	}

	if err = gzipReader.Close(); err != nil {
		return fmt.Errorf("error closing gzip reader: %w", err)
	}

	return WriteOK(stream, nil)
}

// archivePath writes the file or directory at basePath to the tar writer.
// For a single file, the archive contains one entry with the file's base name.
// For a directory, the archive contains the directory and all its contents,
// preserving the top-level directory name.
// Symlinks pointing outside basePath are silently skipped.
func archivePath(tarWriter *tar.Writer, basePath string) error {
	basePath = filepath.Clean(basePath)

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
			return archiveSymlink(tarWriter, basePath, path, relPath)
		case info.IsDir():
			return archiveDir(tarWriter, relPath, info)
		case info.Mode().IsRegular():
			return archiveFile(tarWriter, path, relPath, info)
		default:
			// Skip special files (devices, sockets, etc.)
			return nil
		}
	})
}

func archiveDir(tarWriter *tar.Writer, relPath string, info os.FileInfo) error {
	header, err := tar.FileInfoHeader(info, "")
	if err != nil {
		return fmt.Errorf("error creating tar header for directory %s: %w", relPath, err)
	}

	header.Name = relPath + "/"

	return tarWriter.WriteHeader(header)
}

func archiveFile(tarWriter *tar.Writer, absPath, relPath string, info os.FileInfo) error {
	header, err := tar.FileInfoHeader(info, "")
	if err != nil {
		return fmt.Errorf("error creating tar header for %s: %w", relPath, err)
	}

	header.Name = relPath

	if err = tarWriter.WriteHeader(header); err != nil {
		return fmt.Errorf("error writing tar header for %s: %w", relPath, err)
	}

	f, err := os.Open(absPath)
	if err != nil {
		return fmt.Errorf("error opening %s: %w", absPath, err)
	}
	defer func() {
		_ = f.Close()
	}()

	if _, err = io.Copy(tarWriter, f); err != nil {
		return fmt.Errorf("error writing %s to tar: %w", relPath, err)
	}

	return nil
}

func archiveSymlink(tarWriter *tar.Writer, basePath, absPath, relPath string) error {
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

	return tarWriter.WriteHeader(header)
}

// extractTar reads a tar archive and extracts its contents to destPath.
// All paths are validated to prevent directory traversal attacks.
// Symlinks with targets outside destPath are rejected.
//
// For directories: creates destPath if needed and extracts contents.
// For single files: destPath can be an existing directory (extract file there),
// an existing file (overwrite), or a new file path (parent directory must exist).
func extractTar(tarReader *tar.Reader, destPath string) error {
	destPath = filepath.Clean(destPath)

	// If destPath already exists as a directory, extract normally.
	if info, err := os.Stat(destPath); err == nil && info.IsDir() {
		return extractTarEntries(tarReader, destPath, "")
	}

	// destPath does not exist (or is not a directory). Peek at the first archive entry to
	// determine the source type and decide how to handle the destination.
	header, err := tarReader.Next()
	if err == io.EOF {
		return nil
	}

	if err != nil {
		return fmt.Errorf("error reading tar: %w", err)
	}

	switch header.Typeflag {
	case tar.TypeDir:
		// Source is a directory. Create destPath and extract its contents with the
		// top-level directory name stripped so files land directly inside destPath.
		if err = os.MkdirAll(destPath, os.FileMode(header.Mode)); err != nil {
			return fmt.Errorf("error creating directory %s: %w", destPath, err)
		}

		return extractTarEntries(tarReader, destPath, header.Name)

	case tar.TypeReg:
		return extractSingleFile(tarReader, destPath, header)

	default:
		return ErrFileTransfer{Message: "Transfer of this file type is not supported"}
	}
}

// extractTarEntries extracts tar entries into destPath, optionally stripping a prefix from each
// entry name. All filesystem operations are performed relative to an os.Root anchored at destPath so
// that intermediate symlink path components cannot be followed outside the destination: the kernel
// (via openat2/O_NOFOLLOW semantics) refuses to traverse any symlink that points outside the root.
// This prevents the symlink-chain traversal bypass where lexical validation alone is insufficient.
func extractTarEntries(tarReader *tar.Reader, destPath, stripPrefix string) error {
	root, err := os.OpenRoot(destPath)
	if err != nil {
		return fmt.Errorf("error opening destination root %s: %w", destPath, err)
	}
	defer func() {
		_ = root.Close()
	}()

	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			return nil
		}

		if err != nil {
			return fmt.Errorf("error reading tar: %w", err)
		}

		// If a prefix should be stripped, do that and skip empty entries
		entryName := header.Name
		if stripPrefix != "" {
			entryName = strings.TrimPrefix(header.Name, stripPrefix)
			if entryName == "" {
				// Entry is the top-level directory itself – already created, skip.
				continue
			}
		}

		// Lexical validation as defense-in-depth; root-relative ops below enforce confinement.
		if _, err = validatePath(destPath, entryName); err != nil {
			return err
		}

		// Path relative to the root; the root rejects any escape (symlink or "..").
		relPath := filepath.FromSlash(strings.TrimPrefix(filepath.ToSlash(entryName), "/"))

		switch header.Typeflag {
		case tar.TypeDir:
			if err = mkdirAllRooted(root, relPath, os.FileMode(header.Mode)); err != nil {
				return fmt.Errorf("error creating directory %s: %w", relPath, err)
			}

		case tar.TypeReg:
			if err = extractFile(root, relPath, tarReader, header); err != nil {
				return err
			}

		case tar.TypeSymlink:
			if err = extractSymlink(destPath, root, relPath, header); err != nil {
				return err
			}

		default:
			// Skip unsupported entry types.
			continue
		}
	}
}

func extractFile(root *os.Root, relPath string, tarReader *tar.Reader, header *tar.Header) error {
	// Always remove existing file/symlink at target path before creating new file to prevent abuse of hard links in the archive.
	if err := root.Remove(relPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("error removing existing entry %s: %w", relPath, err)
	}

	f, err := root.OpenFile(relPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY|os.O_TRUNC, os.FileMode(header.Mode))
	if err != nil {
		return fmt.Errorf("error creating file %s: %w", relPath, err)
	}
	defer func() {
		_ = f.Close()
	}()

	if _, err = io.Copy(f, io.LimitReader(tarReader, header.Size)); err != nil {
		return fmt.Errorf("error extracting file %s: %w", relPath, err)
	}

	return nil
}

// extractSingleFile extracts a single regular tar entry to destPath, which is a full file path whose
// parent directory must already exist. Operations are confined to the parent via os.Root.
func extractSingleFile(tarReader *tar.Reader, destPath string, header *tar.Header) error {
	if err := validateParentDir(destPath); err != nil {
		return err
	}

	root, err := os.OpenRoot(filepath.Dir(destPath))
	if err != nil {
		return fmt.Errorf("error opening destination root %s: %w", filepath.Dir(destPath), err)
	}
	defer func() {
		_ = root.Close()
	}()

	return extractFile(root, filepath.Base(destPath), tarReader, header)
}

func extractSymlink(destPath string, root *os.Root, relPath string, header *tar.Header) error {
	targetPath := filepath.Join(destPath, relPath)
	if err := validateSymlink(destPath, header.Linkname, targetPath); err != nil {
		return err
	}

	// Remove existing file/symlink at target path before creating new symlink.
	_ = root.Remove(relPath)

	// The parent components were created through the root, so they are genuine
	// directories (not attacker-planted symlinks); creating the link does not follow
	// any path component and the validated target stays within destPath.
	if err := os.Symlink(header.Linkname, targetPath); err != nil {
		return fmt.Errorf("error creating symlink %s: %w", relPath, err)
	}

	return nil
}

// mkdirAllRooted creates relPath and any missing parents relative to root. Each component is created
// individually so the root can reject traversal through a symlink that escapes the destination.
func mkdirAllRooted(root *os.Root, relPath string, mode os.FileMode) error {
	relPath = filepath.Clean(relPath)

	if relPath == "." || relPath == string(filepath.Separator) {
		return nil
	}

	var current string
	for _, part := range filepath.SplitList(relPath) {
		if part == "" {
			continue
		}

		current = filepath.Join(current, part)
		if err := root.Mkdir(current, mode); err != nil && !os.IsExist(err) {
			return err
		}
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

	if cleanBase == "/" {
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
