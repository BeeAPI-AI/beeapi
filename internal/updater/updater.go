package updater

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	maxMetadataBytes = 2 << 20
	maxChecksumBytes = 4 << 10
	maxArchiveBytes  = 160 << 20
	maxBinaryBytes   = 160 << 20
)

var releaseTagPattern = regexp.MustCompile(`^v?[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?$`)

type Release struct {
	TagName     string    `json:"tag_name"`
	PublishedAt time.Time `json:"published_at"`
}

type Result struct {
	Version   string
	Target    string
	Source    string
	Scheduled bool
}

type Client struct {
	HTTP          *http.Client
	MetadataURLs  []string
	DownloadBases []string
	GOOS          string
	GOARCH        string
	AllowHTTP     bool
}

func DefaultClient() *Client {
	return &Client{
		HTTP: &http.Client{
			Timeout: 20 * time.Second,
			CheckRedirect: func(request *http.Request, via []*http.Request) error {
				if len(via) >= 5 {
					return errors.New("too many redirects")
				}
				if request.URL.Scheme != "https" {
					return errors.New("update redirect must use HTTPS")
				}
				return nil
			},
		},
		MetadataURLs: []string{
			"https://getbeeapi.com/releases/latest.json",
			"https://api.github.com/repos/BeeAPI-AI/beeapi/releases/latest",
		},
		DownloadBases: []string{
			"https://getbeeapi.com/releases/{version}/download",
			"https://github.com/BeeAPI-AI/beeapi/releases/download/{version}",
		},
		GOOS: runtime.GOOS, GOARCH: runtime.GOARCH,
	}
}

func (c *Client) Latest(ctx context.Context) (Release, error) {
	if c == nil {
		return Release{}, errors.New("update client is nil")
	}
	var lastErr error
	for _, raw := range c.MetadataURLs {
		if err := c.validateSourceURL(raw); err != nil {
			lastErr = err
			continue
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
		if err != nil {
			lastErr = err
			continue
		}
		req.Header.Set("Accept", "application/vnd.github+json, application/json")
		req.Header.Set("User-Agent", "beeapi-cli-update/1")
		resp, err := c.httpClient().Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		body, readErr := readLimited(resp.Body, maxMetadataBytes)
		resp.Body.Close()
		if readErr != nil {
			lastErr = readErr
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			lastErr = fmt.Errorf("release metadata returned HTTP %d", resp.StatusCode)
			continue
		}
		var release Release
		if err := json.Unmarshal(body, &release); err != nil {
			lastErr = err
			continue
		}
		release.TagName = normalizeVersion(release.TagName)
		if release.TagName == "" {
			lastErr = errors.New("release metadata has an invalid tag_name")
			continue
		}
		return release, nil
	}
	if lastErr == nil {
		lastErr = errors.New("no update metadata source is configured")
	}
	return Release{}, lastErr
}

func (c *Client) Install(ctx context.Context, release Release, target string) (Result, error) {
	var result Result
	version := normalizeVersion(release.TagName)
	if version == "" {
		return result, errors.New("invalid release version")
	}
	target = strings.TrimSpace(target)
	if target == "" {
		return result, errors.New("current executable path is empty")
	}
	target, err := filepath.Abs(target)
	if err != nil {
		return result, err
	}
	asset, err := AssetName(c.goos(), c.goarch())
	if err != nil {
		return result, err
	}
	tempDir, err := os.MkdirTemp("", "getbeeapi-update-")
	if err != nil {
		return result, err
	}
	defer os.RemoveAll(tempDir)

	archivePath := filepath.Join(tempDir, asset)
	checksumPath := archivePath + ".sha256"
	source, err := c.downloadVerified(ctx, version, asset, archivePath, checksumPath)
	if err != nil {
		return result, err
	}
	binaryName := "beeapi"
	if c.goos() == "windows" {
		binaryName = "beeapi.exe"
	}
	extractedPath := filepath.Join(tempDir, binaryName)
	if strings.HasSuffix(asset, ".zip") {
		err = extractZipBinary(archivePath, binaryName, extractedPath)
	} else {
		err = extractTarGzBinary(archivePath, binaryName, extractedPath)
	}
	if err != nil {
		return result, err
	}
	if err := os.Chmod(extractedPath, 0o755); err != nil && c.goos() != "windows" {
		return result, err
	}

	staging, err := stageBesideTarget(extractedPath, target)
	if err != nil {
		return result, fmt.Errorf("stage update beside current executable: %w", err)
	}
	scheduled, err := replaceExecutable(staging, target)
	if err != nil {
		_ = os.Remove(staging)
		return result, err
	}
	return Result{Version: version, Target: target, Source: source, Scheduled: scheduled}, nil
}

func AssetName(goos, goarch string) (string, error) {
	if goos != "linux" && goos != "darwin" && goos != "windows" {
		return "", fmt.Errorf("unsupported operating system %q", goos)
	}
	if goarch != "amd64" && goarch != "arm64" {
		return "", fmt.Errorf("unsupported architecture %q", goarch)
	}
	extension := ".tar.gz"
	if goos == "windows" {
		extension = ".zip"
	}
	return fmt.Sprintf("beeapi_%s_%s%s", goos, goarch, extension), nil
}

func IsNewer(latest, current string) bool {
	latestVersion, latestOK := parseVersion(latest)
	currentVersion, currentOK := parseVersion(current)
	if !latestOK || !currentOK {
		return false
	}
	return latestVersion.compare(currentVersion) > 0
}

func ValidVersion(raw string) bool {
	_, ok := parseVersion(raw)
	return ok
}

type semanticVersion struct {
	major, minor, patch int
	prerelease          string
}

func parseVersion(raw string) (semanticVersion, bool) {
	raw = strings.TrimPrefix(strings.TrimSpace(raw), "v")
	if index := strings.IndexByte(raw, '+'); index >= 0 {
		raw = raw[:index]
	}
	parts := strings.SplitN(raw, "-", 2)
	numbers := strings.Split(parts[0], ".")
	if len(numbers) != 3 {
		return semanticVersion{}, false
	}
	parsed := make([]int, 3)
	for index, value := range numbers {
		if value == "" || (len(value) > 1 && value[0] == '0') {
			return semanticVersion{}, false
		}
		number, err := strconv.Atoi(value)
		if err != nil || number < 0 {
			return semanticVersion{}, false
		}
		parsed[index] = number
	}
	version := semanticVersion{major: parsed[0], minor: parsed[1], patch: parsed[2]}
	if len(parts) == 2 {
		if strings.TrimSpace(parts[1]) == "" {
			return semanticVersion{}, false
		}
		version.prerelease = parts[1]
	}
	return version, true
}

func (v semanticVersion) compare(other semanticVersion) int {
	for _, pair := range [][2]int{{v.major, other.major}, {v.minor, other.minor}, {v.patch, other.patch}} {
		if pair[0] < pair[1] {
			return -1
		}
		if pair[0] > pair[1] {
			return 1
		}
	}
	if v.prerelease == other.prerelease {
		return 0
	}
	if v.prerelease == "" {
		return 1
	}
	if other.prerelease == "" {
		return -1
	}
	return comparePrerelease(v.prerelease, other.prerelease)
}

func comparePrerelease(left, right string) int {
	lparts, rparts := strings.Split(left, "."), strings.Split(right, ".")
	for index := 0; index < len(lparts) || index < len(rparts); index++ {
		if index >= len(lparts) {
			return -1
		}
		if index >= len(rparts) {
			return 1
		}
		li, lerr := strconv.Atoi(lparts[index])
		ri, rerr := strconv.Atoi(rparts[index])
		switch {
		case lerr == nil && rerr == nil && li < ri:
			return -1
		case lerr == nil && rerr == nil && li > ri:
			return 1
		case lerr == nil && rerr != nil:
			return -1
		case lerr != nil && rerr == nil:
			return 1
		case lparts[index] < rparts[index]:
			return -1
		case lparts[index] > rparts[index]:
			return 1
		}
	}
	return 0
}

func normalizeVersion(raw string) string {
	raw = strings.TrimSpace(raw)
	if !releaseTagPattern.MatchString(raw) {
		return ""
	}
	if !strings.HasPrefix(raw, "v") {
		raw = "v" + raw
	}
	if _, ok := parseVersion(raw); !ok {
		return ""
	}
	return raw
}

func (c *Client) downloadVerified(ctx context.Context, version, asset, archivePath, checksumPath string) (string, error) {
	var lastErr error
	for _, template := range c.DownloadBases {
		base := strings.TrimRight(strings.ReplaceAll(template, "{version}", url.PathEscape(version)), "/")
		if err := c.validateSourceURL(base); err != nil {
			lastErr = err
			continue
		}
		archiveURL, checksumURL := base+"/"+asset, base+"/"+asset+".sha256"
		if err := c.downloadFile(ctx, archiveURL, archivePath, maxArchiveBytes); err != nil {
			lastErr = err
			continue
		}
		if err := c.downloadFile(ctx, checksumURL, checksumPath, maxChecksumBytes); err != nil {
			lastErr = err
			continue
		}
		if err := verifySHA256(archivePath, checksumPath); err != nil {
			lastErr = err
			continue
		}
		return base, nil
	}
	if lastErr == nil {
		lastErr = errors.New("no update download source is configured")
	}
	return "", lastErr
}

func (c *Client) downloadFile(ctx context.Context, raw, destination string, maxBytes int64) error {
	if err := c.validateSourceURL(raw); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/octet-stream")
	req.Header.Set("User-Agent", "beeapi-cli-update/1")
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("download returned HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > maxBytes {
		return errors.New("update artifact exceeds the size limit")
	}
	file, err := os.OpenFile(destination, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	written, copyErr := io.Copy(file, io.LimitReader(resp.Body, maxBytes+1))
	closeErr := file.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if written > maxBytes {
		return errors.New("update artifact exceeds the size limit")
	}
	return nil
}

func verifySHA256(archivePath, checksumPath string) error {
	raw, err := os.ReadFile(checksumPath)
	if err != nil {
		return err
	}
	expected := strings.TrimSpace(string(raw))
	if len(expected) != sha256.Size*2 {
		return errors.New("release checksum has an invalid format")
	}
	decoded, err := hex.DecodeString(expected)
	if err != nil {
		return errors.New("release checksum has an invalid format")
	}
	file, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	hash := sha256.New()
	_, copyErr := io.Copy(hash, file)
	closeErr := file.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if !equalBytes(hash.Sum(nil), decoded) {
		return errors.New("SHA-256 verification failed")
	}
	return nil
}

func extractTarGzBinary(archivePath, binaryName, destination string) error {
	file, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer file.Close()
	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer gzipReader.Close()
	reader := tar.NewReader(gzipReader)
	found := false
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if header.Name != binaryName {
			continue
		}
		if found || header.Typeflag != tar.TypeReg || header.Size < 1 || header.Size > maxBinaryBytes {
			return errors.New("release archive contains an invalid executable")
		}
		if err := copyExactExecutable(reader, header.Size, destination); err != nil {
			return err
		}
		found = true
	}
	if !found {
		return errors.New("release archive is missing the beeapi executable")
	}
	return nil
}

func extractZipBinary(archivePath, binaryName, destination string) error {
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return err
	}
	defer reader.Close()
	found := false
	for _, file := range reader.File {
		if file.Name != binaryName {
			continue
		}
		if found || file.FileInfo().Mode()&os.ModeType != 0 || file.UncompressedSize64 < 1 || file.UncompressedSize64 > maxBinaryBytes {
			return errors.New("release archive contains an invalid executable")
		}
		source, err := file.Open()
		if err != nil {
			return err
		}
		err = copyExactExecutable(source, int64(file.UncompressedSize64), destination)
		closeErr := source.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		found = true
	}
	if !found {
		return errors.New("release archive is missing the beeapi executable")
	}
	return nil
}

func copyExactExecutable(source io.Reader, size int64, destination string) error {
	file, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o700)
	if err != nil {
		return err
	}
	written, copyErr := io.CopyN(file, source, size)
	closeErr := file.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if written != size {
		return errors.New("release executable is truncated")
	}
	return nil
}

func stageBesideTarget(source, target string) (string, error) {
	directory := filepath.Dir(target)
	file, err := os.CreateTemp(directory, ".beeapi-update-*")
	if err != nil {
		return "", err
	}
	staging := file.Name()
	if err := file.Close(); err != nil {
		_ = os.Remove(staging)
		return "", err
	}
	sourceFile, err := os.Open(source)
	if err != nil {
		_ = os.Remove(staging)
		return "", err
	}
	destination, err := os.OpenFile(staging, os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		sourceFile.Close()
		_ = os.Remove(staging)
		return "", err
	}
	_, copyErr := io.Copy(destination, io.LimitReader(sourceFile, maxBinaryBytes+1))
	sourceErr := sourceFile.Close()
	destinationErr := destination.Close()
	if copyErr != nil || sourceErr != nil || destinationErr != nil {
		_ = os.Remove(staging)
		return "", errors.Join(copyErr, sourceErr, destinationErr)
	}
	if err := os.Chmod(staging, 0o755); err != nil && runtime.GOOS != "windows" {
		_ = os.Remove(staging)
		return "", err
	}
	return staging, nil
}

func readLimited(reader io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("response exceeds the size limit")
	}
	return data, nil
}

func (c *Client) validateSourceURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Hostname() == "" || parsed.User != nil || parsed.Fragment != "" {
		return errors.New("update source URL is invalid")
	}
	if parsed.Scheme != "https" && !(c.AllowHTTP && parsed.Scheme == "http") {
		return errors.New("update source must use HTTPS")
	}
	return nil
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 20 * time.Second}
}

func (c *Client) goos() string {
	if strings.TrimSpace(c.GOOS) != "" {
		return c.GOOS
	}
	return runtime.GOOS
}

func (c *Client) goarch() string {
	if strings.TrimSpace(c.GOARCH) != "" {
		return c.GOARCH
	}
	return runtime.GOARCH
}

func equalBytes(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	difference := byte(0)
	for index := range left {
		difference |= left[index] ^ right[index]
	}
	return difference == 0
}
