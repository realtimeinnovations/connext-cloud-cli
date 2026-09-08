// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

package connext

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/realtimeinnovations/connext-cloud-cli/internal/terminal"
)

// Cached installers are always verified before reuse. Partial downloads never
// occupy the cache filename. The part file and final file share a filesystem.
func cachedInstaller(ctx context.Context, url, digest string, client *http.Client, out io.Writer) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	home, err := UserHomeDir()
	if err != nil {
		return "", err
	}
	platform, _ := Platform()
	preferred := os.TempDir()
	if platform == "linux" || platform == "windows" {
		preferred = filepath.Join(home, "Downloads")
	}
	directory, err := writableDownloadDirectory(preferred)
	if err != nil {
		directory, err = writableDownloadDirectory(os.TempDir())
		if err != nil {
			return "", err
		}
	}
	target := filepath.Join(directory, filepath.Base(url))
	if info, err := os.Lstat(target); err == nil {
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("installer cache path is not a regular file: %s", target)
		}
		if verifyInstallerContext(ctx, target, digest) == nil {
			fmt.Fprintln(out, "Using verified cached Connext installer.")
			return target, nil
		}
	} else if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	part, err := os.CreateTemp(directory, ".rticloud-installer-*.part")
	if err != nil {
		return "", err
	}
	defer os.Remove(part.Name())
	defer part.Close()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Minute}
	}
	response, err := client.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("Connext download failed: %s", response.Status)
	}
	fmt.Fprintln(out, "Downloading Connext Professional installer...")
	progress := &downloadProgress{out: out, total: response.ContentLength, inline: terminal.CanAnimate(out)}
	_, copyErr := io.Copy(io.MultiWriter(part, progress), response.Body)
	progress.finish()
	if err := copyErr; err != nil {
		return "", err
	}
	if response.ContentLength >= 0 && progress.received != response.ContentLength {
		return "", io.ErrUnexpectedEOF
	}
	if err := part.Sync(); err != nil {
		return "", err
	}
	if err := part.Close(); err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := verifyInstallerContext(ctx, part.Name(), digest); err != nil {
		return "", err
	}
	// Never traverse an existing symlink or replace unrelated non-file entries.
	if info, err := os.Lstat(target); err == nil {
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("installer cache path is not a regular file: %s", target)
		}
		// Windows rename does not replace existing destinations. Our installation
		// lock serializes cache writers for this product/version/platform.
		if err := os.Remove(target); err != nil {
			return "", err
		}
	} else if !os.IsNotExist(err) {
		return "", err
	}
	if err := os.Rename(part.Name(), target); err != nil {
		return "", err
	}
	fmt.Fprintln(out, "Connext installer download verified.")
	return target, nil
}

func writableDownloadDirectory(directory string) (string, error) {
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", err
	}
	probe, err := os.CreateTemp(directory, ".rticloud-write-check-*")
	if err != nil {
		return "", err
	}
	err = probe.Close()
	_ = os.Remove(probe.Name())
	return directory, err
}

type downloadProgress struct {
	out             io.Writer
	total, received int64
	lastPercent     int
	lastReport      time.Time
	inline          bool
	rendered        bool
}

func (p *downloadProgress) Write(data []byte) (int, error) {
	p.received += int64(len(data))
	if p.total > 0 {
		percent := min(100, int(p.received*100/p.total)) / 5 * 5
		if percent > p.lastPercent {
			p.report(fmt.Sprintf("Download: %d%%", percent))
			p.lastPercent = percent
		}
	} else if time.Since(p.lastReport) >= time.Second {
		p.report(fmt.Sprintf("Downloaded %.1f MiB", float64(p.received)/(1024*1024)))
		p.lastReport = time.Now()
	}
	return len(data), nil
}

func (p *downloadProgress) report(message string) {
	if p.inline {
		fmt.Fprint(p.out, "\r"+message)
	} else {
		fmt.Fprintln(p.out, message)
	}
	p.rendered = true
}

func (p *downloadProgress) finish() {
	if p.inline && p.rendered {
		fmt.Fprintln(p.out)
		p.rendered = false
	}
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}
