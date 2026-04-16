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
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/xtaci/smux"
)

func TestFileTransfer_DownloadSingleFile(t *testing.T) {
	client, deviceClient, _ := NewEdgeMock(t)
	deviceClient.WithHandler(MessageTypeFile, HandleFileTransfer)
	ctx := t.Context()

	// Create a file on the "device" side.
	deviceDir := t.TempDir()
	content := []byte("hello world from device")
	if err := os.WriteFile(filepath.Join(deviceDir, "test.txt"), content, 0644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	// Download the file.
	clientDir := t.TempDir()
	if err := client.DownloadFile(ctx, filepath.Join(deviceDir, "test.txt"), clientDir); err != nil {
		t.Fatalf("DownloadFile failed: %v", err)
	}

	// Verify the file was downloaded.
	got, err := os.ReadFile(filepath.Join(clientDir, "test.txt"))
	if err != nil {
		t.Fatalf("failed to read downloaded file: %v", err)
	}

	if !bytes.Equal(got, content) {
		t.Fatalf("downloaded content mismatch: got %q, want %q", got, content)
	}
}

func TestFileTransfer_DownloadDirectory(t *testing.T) {
	client, deviceClient, _ := NewEdgeMock(t)
	deviceClient.WithHandler(MessageTypeFile, HandleFileTransfer)
	ctx := t.Context()

	// Create a directory tree on the "device" side.
	deviceDir := t.TempDir()
	tree := filepath.Join(deviceDir, "project")

	dirs := []string{
		filepath.Join(tree, "src"),
		filepath.Join(tree, "src", "nested"),
		filepath.Join(tree, "docs"),
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatalf("failed to create dir %s: %v", d, err)
		}
	}

	files := map[string][]byte{
		filepath.Join(tree, "README.md"):                []byte("# Project"),
		filepath.Join(tree, "src", "main.go"):           []byte("package main"),
		filepath.Join(tree, "src", "nested", "lib.go"):  []byte("package nested"),
		filepath.Join(tree, "docs", "guide.txt"):        []byte("usage guide"),
	}
	for path, content := range files {
		if err := os.WriteFile(path, content, 0644); err != nil {
			t.Fatalf("failed to create file %s: %v", path, err)
		}
	}

	// Download the directory.
	clientDir := t.TempDir()
	if err := client.DownloadFile(ctx, tree, clientDir); err != nil {
		t.Fatalf("DownloadFile failed: %v", err)
	}

	// Verify all files were downloaded.
	for origPath, wantContent := range files {
		relPath, _ := filepath.Rel(filepath.Dir(tree), origPath)
		gotContent, err := os.ReadFile(filepath.Join(clientDir, relPath))
		if err != nil {
			t.Fatalf("failed to read %s: %v", relPath, err)
		}

		if !bytes.Equal(gotContent, wantContent) {
			t.Errorf("content mismatch for %s: got %q, want %q", relPath, gotContent, wantContent)
		}
	}
}

func TestFileTransfer_UploadSingleFile(t *testing.T) {
	client, deviceClient, _ := NewEdgeMock(t)
	deviceClient.WithHandler(MessageTypeFile, HandleFileTransfer)
	ctx := t.Context()

	// Create a file on the "client" side.
	clientDir := t.TempDir()
	content := []byte("hello world from client")
	if err := os.WriteFile(filepath.Join(clientDir, "upload.txt"), content, 0644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	// Upload the file.
	deviceDir := t.TempDir()
	if err := client.UploadFile(ctx, filepath.Join(clientDir, "upload.txt"), deviceDir); err != nil {
		t.Fatalf("UploadFile failed: %v", err)
	}

	// Verify the file was uploaded.
	got, err := os.ReadFile(filepath.Join(deviceDir, "upload.txt"))
	if err != nil {
		t.Fatalf("failed to read uploaded file: %v", err)
	}

	if !bytes.Equal(got, content) {
		t.Fatalf("uploaded content mismatch: got %q, want %q", got, content)
	}
}

func TestFileTransfer_UploadDirectory(t *testing.T) {
	client, deviceClient, _ := NewEdgeMock(t)
	deviceClient.WithHandler(MessageTypeFile, HandleFileTransfer)
	ctx := t.Context()

	// Create a directory tree on the "client" side.
	clientDir := t.TempDir()
	tree := filepath.Join(clientDir, "myapp")

	dirs := []string{
		filepath.Join(tree, "bin"),
		filepath.Join(tree, "config"),
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatalf("failed to create dir %s: %v", d, err)
		}
	}

	files := map[string][]byte{
		filepath.Join(tree, "bin", "app"):            []byte("binary content"),
		filepath.Join(tree, "config", "app.yaml"):    []byte("key: value"),
		filepath.Join(tree, "config", "secrets.env"):  []byte("SECRET=abc"),
	}
	for path, content := range files {
		if err := os.WriteFile(path, content, 0644); err != nil {
			t.Fatalf("failed to create file %s: %v", path, err)
		}
	}

	// Upload the directory.
	deviceDir := t.TempDir()
	if err := client.UploadFile(ctx, tree, deviceDir); err != nil {
		t.Fatalf("UploadFile failed: %v", err)
	}

	// Verify all files were uploaded.
	for origPath, wantContent := range files {
		relPath, _ := filepath.Rel(filepath.Dir(tree), origPath)
		gotContent, err := os.ReadFile(filepath.Join(deviceDir, relPath))
		if err != nil {
			t.Fatalf("failed to read %s: %v", relPath, err)
		}

		if !bytes.Equal(gotContent, wantContent) {
			t.Errorf("content mismatch for %s: got %q, want %q", relPath, gotContent, wantContent)
		}
	}
}

func TestFileTransfer_DownloadNonExistentPath(t *testing.T) {
	client, deviceClient, _ := NewEdgeMock(t)
	deviceClient.WithHandler(MessageTypeFile, HandleFileTransfer)
	ctx := t.Context()

	clientDir := t.TempDir()
	err := client.DownloadFile(ctx, "/nonexistent/path/file.txt", clientDir)
	if err == nil {
		t.Fatal("expected error for non-existent path, got nil")
	}
}

func TestFileTransfer_UploadToNonExistentPath(t *testing.T) {
	client, deviceClient, _ := NewEdgeMock(t)
	deviceClient.WithHandler(MessageTypeFile, HandleFileTransfer)
	ctx := t.Context()

	clientDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(clientDir, "test.txt"), []byte("data"), 0644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	err := client.UploadFile(ctx, filepath.Join(clientDir, "test.txt"), "/nonexistent/destination")
	if err == nil {
		t.Fatal("expected error for non-existent destination, got nil")
	}
}

func TestFileTransfer_UploadToFilePath(t *testing.T) {
	client, deviceClient, _ := NewEdgeMock(t)
	deviceClient.WithHandler(MessageTypeFile, HandleFileTransfer)
	ctx := t.Context()

	// Create a source file.
	clientDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(clientDir, "test.txt"), []byte("data"), 0644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	// Try to upload to a file (not a directory).
	deviceDir := t.TempDir()
	deviceFile := filepath.Join(deviceDir, "not_a_dir")
	if err := os.WriteFile(deviceFile, []byte("existing"), 0644); err != nil {
		t.Fatalf("failed to create device file: %v", err)
	}

	err := client.UploadFile(ctx, filepath.Join(clientDir, "test.txt"), deviceFile)
	if err == nil {
		t.Fatal("expected error when uploading to a file path, got nil")
	}
}

func TestFileTransfer_RelativePathRejected(t *testing.T) {
	client, deviceClient, _ := NewEdgeMock(t)
	deviceClient.WithHandler(MessageTypeFile, HandleFileTransfer)
	ctx := t.Context()

	clientDir := t.TempDir()
	err := client.DownloadFile(ctx, "relative/path/file.txt", clientDir)
	if err == nil {
		t.Fatal("expected error for relative path, got nil")
	}
}

func TestFileTransfer_EmptyDirectory(t *testing.T) {
	client, deviceClient, _ := NewEdgeMock(t)
	deviceClient.WithHandler(MessageTypeFile, HandleFileTransfer)
	ctx := t.Context()

	// Create an empty directory on the "device" side.
	deviceDir := t.TempDir()
	emptyDir := filepath.Join(deviceDir, "empty")
	if err := os.MkdirAll(emptyDir, 0755); err != nil {
		t.Fatalf("failed to create empty dir: %v", err)
	}

	// Download the empty directory.
	clientDir := t.TempDir()
	if err := client.DownloadFile(ctx, emptyDir, clientDir); err != nil {
		t.Fatalf("DownloadFile failed: %v", err)
	}

	// Verify the empty directory was created.
	info, err := os.Stat(filepath.Join(clientDir, "empty"))
	if err != nil {
		t.Fatalf("empty directory not found: %v", err)
	}

	if !info.IsDir() {
		t.Fatal("expected a directory")
	}
}

func TestFileTransfer_LargeFile(t *testing.T) {
	client, deviceClient, _ := NewEdgeMock(t)
	deviceClient.WithHandler(MessageTypeFile, HandleFileTransfer)
	ctx := t.Context()

	// Create a 2MB file with random content.
	deviceDir := t.TempDir()
	content := make([]byte, 2*1024*1024)
	_, _ = rand.Read(content)

	if err := os.WriteFile(filepath.Join(deviceDir, "large.bin"), content, 0644); err != nil {
		t.Fatalf("failed to create large file: %v", err)
	}

	expectedHash := sha256.Sum256(content)

	// Download the file.
	clientDir := t.TempDir()
	if err := client.DownloadFile(ctx, filepath.Join(deviceDir, "large.bin"), clientDir); err != nil {
		t.Fatalf("DownloadFile failed: %v", err)
	}

	// Verify checksum.
	got, err := os.ReadFile(filepath.Join(clientDir, "large.bin"))
	if err != nil {
		t.Fatalf("failed to read downloaded file: %v", err)
	}

	gotHash := sha256.Sum256(got)
	if expectedHash != gotHash {
		t.Fatal("checksum mismatch for large file transfer")
	}
}

func TestFileTransfer_FilePermissions(t *testing.T) {
	client, deviceClient, _ := NewEdgeMock(t)
	deviceClient.WithHandler(MessageTypeFile, HandleFileTransfer)
	ctx := t.Context()

	deviceDir := t.TempDir()
	filePath := filepath.Join(deviceDir, "script.sh")

	if err := os.WriteFile(filePath, []byte("#!/bin/sh\necho hi"), 0755); err != nil {
		t.Fatalf("failed to create file: %v", err)
	}

	clientDir := t.TempDir()
	if err := client.DownloadFile(ctx, filePath, clientDir); err != nil {
		t.Fatalf("DownloadFile failed: %v", err)
	}

	info, err := os.Stat(filepath.Join(clientDir, "script.sh"))
	if err != nil {
		t.Fatalf("failed to stat downloaded file: %v", err)
	}

	// Check that executable bit is preserved.
	if info.Mode().Perm()&0111 == 0 {
		t.Errorf("expected executable permissions, got %v", info.Mode().Perm())
	}
}

func TestFileTransfer_SymlinkWithinDirectory(t *testing.T) {
	client, deviceClient, _ := NewEdgeMock(t)
	deviceClient.WithHandler(MessageTypeFile, HandleFileTransfer)
	ctx := t.Context()

	deviceDir := t.TempDir()
	tree := filepath.Join(deviceDir, "project")

	if err := os.MkdirAll(filepath.Join(tree, "src"), 0755); err != nil {
		t.Fatalf("failed to create dir: %v", err)
	}

	if err := os.WriteFile(filepath.Join(tree, "src", "main.go"), []byte("package main"), 0644); err != nil {
		t.Fatalf("failed to create file: %v", err)
	}

	// Create a symlink within the tree.
	if err := os.Symlink(filepath.Join(tree, "src", "main.go"), filepath.Join(tree, "link.go")); err != nil {
		t.Fatalf("failed to create symlink: %v", err)
	}

	clientDir := t.TempDir()
	if err := client.DownloadFile(ctx, tree, clientDir); err != nil {
		t.Fatalf("DownloadFile failed: %v", err)
	}

	// Verify the symlink was preserved.
	linkTarget, err := os.Readlink(filepath.Join(clientDir, "project", "link.go"))
	if err != nil {
		t.Fatalf("symlink not found in downloaded directory: %v", err)
	}

	if linkTarget == "" {
		t.Fatal("symlink target is empty")
	}
}

func TestFileTransfer_SymlinkEscapingSkipped(t *testing.T) {
	client, deviceClient, _ := NewEdgeMock(t)
	deviceClient.WithHandler(MessageTypeFile, HandleFileTransfer)
	ctx := t.Context()

	deviceDir := t.TempDir()
	tree := filepath.Join(deviceDir, "project")

	if err := os.MkdirAll(tree, 0755); err != nil {
		t.Fatalf("failed to create dir: %v", err)
	}

	if err := os.WriteFile(filepath.Join(tree, "file.txt"), []byte("data"), 0644); err != nil {
		t.Fatalf("failed to create file: %v", err)
	}

	// Create a symlink pointing outside the tree.
	if err := os.Symlink("/etc/hosts", filepath.Join(tree, "escape.txt")); err != nil {
		t.Fatalf("failed to create symlink: %v", err)
	}

	clientDir := t.TempDir()
	if err := client.DownloadFile(ctx, tree, clientDir); err != nil {
		t.Fatalf("DownloadFile failed: %v", err)
	}

	// The regular file should exist.
	if _, err := os.Stat(filepath.Join(clientDir, "project", "file.txt")); err != nil {
		t.Fatalf("regular file should exist: %v", err)
	}

	// The escaping symlink should be skipped.
	if _, err := os.Lstat(filepath.Join(clientDir, "project", "escape.txt")); err == nil {
		t.Fatal("escaping symlink should have been skipped")
	}
}

func TestValidatePath(t *testing.T) {
	base := "/tmp/extract"

	tests := []struct {
		name     string
		entry    string
		wantErr  bool
		wantPath string
	}{
		{
			name:     "simple file",
			entry:    "file.txt",
			wantErr:  false,
			wantPath: "/tmp/extract/file.txt",
		},
		{
			name:     "nested file",
			entry:    "dir/subdir/file.txt",
			wantErr:  false,
			wantPath: "/tmp/extract/dir/subdir/file.txt",
		},
		{
			name:    "path traversal with dot-dot",
			entry:   "../etc/passwd",
			wantErr: true,
		},
		{
			name:    "path traversal mid-path",
			entry:   "dir/../../etc/passwd",
			wantErr: true,
		},
		{
			name:     "absolute path in entry stays within base",
			entry:    "/etc/passwd",
			wantErr:  false,
			wantPath: "/tmp/extract/etc/passwd",
		},
		{
			name:     "base path itself",
			entry:    ".",
			wantErr:  false,
			wantPath: "/tmp/extract",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := validatePath(base, tt.entry)
			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error for entry %q, got path %q", tt.entry, got)
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error for entry %q: %v", tt.entry, err)
				return
			}

			if got != tt.wantPath {
				t.Errorf("path mismatch: got %q, want %q", got, tt.wantPath)
			}
		})
	}
}

// TestFileTransfer_DownloadMultistreamDeadlock verifies that DownloadFile does not deadlock
// when the device-side stream remains open after sending gzip data.
// Without gzipReader.Multistream(false), the gzip reader blocks trying to read the next
// gzip member header from the still-open stream.
func TestFileTransfer_DownloadMultistreamDeadlock(t *testing.T) {
	client, deviceClient, _ := NewEdgeMock(t)

	handlerDone := make(chan struct{})
	t.Cleanup(func() { close(handlerDone) })

	// Custom handler that writes gzip(tar(file)) but keeps the stream open.
	deviceClient.WithHandler(MessageTypeFile, func(ctx context.Context, stream *smux.Stream, payload []byte) error {
		var req FileTransferRequest
		if err := json.Unmarshal(payload, &req); err != nil {
			return WriteError(stream, err)
		}

		if err := WriteOK(stream, nil); err != nil {
			return err
		}

		gzipWriter := gzip.NewWriter(stream)
		tarWriter := tar.NewWriter(gzipWriter)

		if err := archivePath(tarWriter, req.Path); err != nil {
			return err
		}
		if err := tarWriter.Close(); err != nil {
			return err
		}
		if err := gzipWriter.Close(); err != nil {
			return err
		}

		// Keep the handler alive so handleStream doesn't close the stream.
		// This simulates a long-lived connection where the stream stays open.
		select {
		case <-ctx.Done():
		case <-handlerDone:
		}
		return nil
	})

	deviceDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(deviceDir, "test.txt"), []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}

	clientDir := t.TempDir()

	errCh := make(chan error, 1)
	go func() {
		errCh <- client.DownloadFile(t.Context(), filepath.Join(deviceDir, "test.txt"), clientDir)
	}()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("DownloadFile failed: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("DownloadFile deadlocked - gzip.Reader.Multistream(false) is missing")
	}

	// Verify the file was downloaded correctly.
	got, err := os.ReadFile(filepath.Join(clientDir, "test.txt"))
	if err != nil {
		t.Fatalf("failed to read downloaded file: %v", err)
	}
	if !bytes.Equal(got, []byte("hello")) {
		t.Fatalf("content mismatch: got %q", got)
	}
}

func TestValidateSymlink(t *testing.T) {
	base := "/tmp/extract"

	tests := []struct {
		name       string
		linkTarget string
		linkPath   string
		wantErr    bool
	}{
		{
			name:       "relative link within base",
			linkTarget: "other.txt",
			linkPath:   "/tmp/extract/dir/link.txt",
			wantErr:    false,
		},
		{
			name:       "relative link escaping base",
			linkTarget: "../../etc/passwd",
			linkPath:   "/tmp/extract/dir/link.txt",
			wantErr:    true,
		},
		{
			name:       "absolute link within base",
			linkTarget: "/tmp/extract/other.txt",
			linkPath:   "/tmp/extract/link.txt",
			wantErr:    false,
		},
		{
			name:       "absolute link escaping base",
			linkTarget: "/etc/passwd",
			linkPath:   "/tmp/extract/link.txt",
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateSymlink(base, tt.linkTarget, tt.linkPath)
			if tt.wantErr && err == nil {
				t.Errorf("expected error for link %q -> %q", tt.linkPath, tt.linkTarget)
			}

			if !tt.wantErr && err != nil {
				t.Errorf("unexpected error for link %q -> %q: %v", tt.linkPath, tt.linkTarget, err)
			}
		})
	}
}
