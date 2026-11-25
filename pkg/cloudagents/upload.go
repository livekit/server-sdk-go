// Copyright 2025 LiveKit, Inc.
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

package cloudagents

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"mime/multipart"
	"net/http"

	"github.com/livekit/protocol/livekit"
)

var (
	defaultExcludePatterns = []string{
		"**/Dockerfile",
		"**/.dockerignore",
		"**/.gitignore",
		"**/.git",
		"**/.git/**",
		"**/node_modules",
		"**/node_modules/**",
		"**/.env",
		"**/.env.*",
		"**/.venv",
		"**/.venv/**",
		"**/venv",
		"**/venv/**",
	}

	ignoreFilePatterns = []string{
		".gitignore",
		".dockerignore",
	}
)

// UploadSource uploads source, as a gzipped tarball, to the upload target of a
// CreateAgent or DeployAgent response: presignedPostRequest when it is set,
// else presignedURL. The tarball leaves out excludeFiles and what
// CreateSourceTarball always leaves out. The upload goes through the Client's
// HTTP client, and ctx bounds it.
func (c *Client) UploadSource(
	ctx context.Context,
	source fs.FS,
	presignedURL string,
	presignedPostRequest *livekit.PresignedPostRequest,
	excludeFiles []string,
) error {
	return uploadSource(ctx, c.httpClient, source, presignedURL, presignedPostRequest, excludeFiles)
}

func uploadSource(
	ctx context.Context,
	client *http.Client,
	directory fs.FS,
	presignedUrl string,
	presignedPostRequest *livekit.PresignedPostRequest,
	excludeFiles []string,
) error {
	var buf bytes.Buffer
	if err := CreateSourceTarball(directory, excludeFiles, &buf); err != nil {
		return fmt.Errorf("failed to sanitize source: %w", err)
	}
	if presignedPostRequest != nil {
		if err := MultipartUploadContext(ctx, client, presignedPostRequest.Url, presignedPostRequest.Values, &buf); err != nil {
			return fmt.Errorf("multipart upload failed: %w", err)
		}
	} else {
		if err := upload(ctx, client, presignedUrl, &buf); err != nil {
			return fmt.Errorf("upload failed: %w", err)
		}
	}
	return nil
}

func upload(ctx context.Context, client *http.Client, presignedUrl string, buf *bytes.Buffer) error {
	req, err := http.NewRequestWithContext(ctx, "PUT", presignedUrl, buf)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/gzip")
	req.ContentLength = int64(buf.Len())
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to upload tarball: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to upload tarball: %d: %s", resp.StatusCode, body)
	}
	return nil
}

// MultipartUpload is MultipartUploadContext with context.Background() and
// http.DefaultClient.
func MultipartUpload(presignedURL string, fields map[string]string, buf *bytes.Buffer) error {
	return MultipartUploadContext(context.Background(), http.DefaultClient, presignedURL, fields, buf)
}

// MultipartUploadContext posts buf as the file of a multipart form, with
// fields, to presignedURL, a presigned POST target. It sends the form through
// client, or http.DefaultClient when client is nil, and ctx bounds it.
func MultipartUploadContext(ctx context.Context, client *http.Client, presignedURL string, fields map[string]string, buf *bytes.Buffer) error {
	if client == nil {
		client = http.DefaultClient
	}
	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	fileName, ok := fields["key"]
	if !ok {
		fileName = "upload.tar.gz"
	}
	for k, v := range fields {
		if err := w.WriteField(k, v); err != nil {
			return err
		}
	}
	part, err := w.CreateFormFile("file", fileName)
	if err != nil {
		return err
	}
	if _, err := io.Copy(part, buf); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", presignedURL, &b)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusNoContent {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to upload tarball: %d: %s", resp.StatusCode, respBody)
	}
	return nil
}
