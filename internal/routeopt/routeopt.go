package routeopt

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/BeeAPI-AI/beeapi/internal/state"
)

const releaseAPI = "https://api.github.com/repos/XIU2/CloudflareSpeedTest/releases/latest"

type release struct {
	TagName string `json:"tag_name"`
	Assets  []struct {
		Name        string `json:"name"`
		DownloadURL string `json:"browser_download_url"`
		Digest      string `json:"digest"`
	} `json:"assets"`
}

func EnsureCFST(ctx context.Context, output io.Writer) (string, string, error) {
	if path, err := exec.LookPath(executableName()); err == nil {
		return path, "PATH", nil
	}
	cache, err := cacheDir()
	if err != nil {
		return "", "", err
	}
	versionPath := filepath.Join(cache, "cfst", "version")
	binaryPath := filepath.Join(cache, "cfst", executableName())
	if b, err := os.ReadFile(versionPath); err == nil {
		if info, statErr := os.Stat(binaryPath); statErr == nil && info.Mode().IsRegular() {
			return binaryPath, strings.TrimSpace(string(b)), nil
		}
	}
	fmt.Fprintln(output, "正在从 XIU2/CloudflareSpeedTest 官方发行页获取测速组件…")
	client := &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 || req.URL.Scheme != "https" || !trustedDownloadHost(req.URL.Hostname()) {
				return errors.New("测速组件下载发生了不受信任的重定向")
			}
			return nil
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, releaseAPI, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "beeapi-cli/1")
	resp, err := client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("GitHub 发行接口返回 HTTP %d", resp.StatusCode)
	}
	var rel release
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&rel); err != nil {
		return "", "", err
	}
	assetName, err := platformAssetName()
	if err != nil {
		return "", "", err
	}
	var downloadURL, digest string
	for _, asset := range rel.Assets {
		if asset.Name == assetName {
			downloadURL, digest = asset.DownloadURL, asset.Digest
			break
		}
	}
	if downloadURL == "" || !strings.HasPrefix(digest, "sha256:") {
		return "", "", fmt.Errorf("发行版 %s 缺少 %s 或 SHA-256 摘要", rel.TagName, assetName)
	}
	archivePath, err := downloadVerified(ctx, client, downloadURL, strings.TrimPrefix(digest, "sha256:"))
	if err != nil {
		return "", "", err
	}
	defer os.Remove(archivePath)
	dest := filepath.Join(cache, "cfst")
	if err := os.MkdirAll(dest, 0o700); err != nil {
		return "", "", err
	}
	if strings.HasSuffix(assetName, ".zip") {
		err = extractZip(archivePath, dest)
	} else {
		err = extractTarGz(archivePath, dest)
	}
	if err != nil {
		return "", "", err
	}
	if err := os.Chmod(binaryPath, 0o700); err != nil {
		return "", "", err
	}
	if err := state.AtomicWrite(versionPath, []byte(rel.TagName+"\n"), 0o600); err != nil {
		return "", "", err
	}
	return binaryPath, rel.TagName, nil
}

func platformAssetName() (string, error) {
	arch := runtime.GOARCH
	if arch != "amd64" && arch != "arm64" {
		return "", fmt.Errorf("暂不支持 %s/%s 自动安装 CFST", runtime.GOOS, arch)
	}
	switch runtime.GOOS {
	case "linux":
		return "cfst_linux_" + arch + ".tar.gz", nil
	case "darwin":
		return "cfst_darwin_" + arch + ".zip", nil
	case "windows":
		return "cfst_windows_" + arch + ".zip", nil
	default:
		return "", fmt.Errorf("暂不支持 %s 自动安装 CFST", runtime.GOOS)
	}
}

func executableName() string {
	if runtime.GOOS == "windows" {
		return "cfst.exe"
	}
	return "cfst"
}

func cacheDir() (string, error) {
	if override := strings.TrimSpace(os.Getenv("GETBEE_CACHE")); override != "" {
		return override, nil
	}
	base, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "getbeeapi"), nil
}

func downloadVerified(ctx context.Context, client *http.Client, rawURL, expected string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "https" || (u.Hostname() != "github.com" && u.Hostname() != "objects.githubusercontent.com") {
		return "", errors.New("测速组件下载地址不是受信任的 GitHub HTTPS 地址")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "beeapi-cli/1")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("下载测速组件失败: HTTP %d", resp.StatusCode)
	}
	tmp, err := os.CreateTemp("", "getbee-cfst-*")
	if err != nil {
		return "", err
	}
	path := tmp.Name()
	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(tmp, hash), io.LimitReader(resp.Body, 128<<20))
	closeErr := tmp.Close()
	if copyErr != nil || closeErr != nil {
		os.Remove(path)
		return "", errors.Join(copyErr, closeErr)
	}
	if written >= 128<<20 {
		os.Remove(path)
		return "", errors.New("测速组件异常地超过 128MB")
	}
	actual := hex.EncodeToString(hash.Sum(nil))
	if !strings.EqualFold(actual, expected) {
		os.Remove(path)
		return "", fmt.Errorf("测速组件 SHA-256 校验失败: 期望 %s，实际 %s", expected, actual)
	}
	return path, nil
}

func trustedDownloadHost(host string) bool {
	switch strings.ToLower(host) {
	case "github.com", "objects.githubusercontent.com", "release-assets.githubusercontent.com":
		return true
	default:
		return false
	}
}

func extractZip(path, dest string) error {
	reader, err := zip.OpenReader(path)
	if err != nil {
		return err
	}
	defer reader.Close()
	for _, file := range reader.File {
		name := filepath.Base(file.Name)
		if !allowedExtract(name) || file.FileInfo().IsDir() {
			continue
		}
		rc, err := file.Open()
		if err != nil {
			return err
		}
		data, readErr := io.ReadAll(io.LimitReader(rc, 64<<20))
		rc.Close()
		if readErr != nil {
			return readErr
		}
		mode := os.FileMode(0o600)
		if name == executableName() || name == "cfst" || name == "cfst.exe" {
			name = executableName()
			mode = 0o700
		}
		if err := state.AtomicWrite(filepath.Join(dest, name), data, mode); err != nil {
			return err
		}
	}
	return requireExtracted(dest)
}

func extractTarGz(path, dest string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer gz.Close()
	tarReader := tar.NewReader(gz)
	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		name := filepath.Base(header.Name)
		if header.Typeflag != tar.TypeReg || !allowedExtract(name) {
			continue
		}
		data, err := io.ReadAll(io.LimitReader(tarReader, 64<<20))
		if err != nil {
			return err
		}
		mode := os.FileMode(0o600)
		if name == "cfst" || name == "cfst.exe" {
			name = executableName()
			mode = 0o700
		}
		if err := state.AtomicWrite(filepath.Join(dest, name), data, mode); err != nil {
			return err
		}
	}
	return requireExtracted(dest)
}

func allowedExtract(name string) bool {
	switch strings.ToLower(name) {
	case "cfst", "cfst.exe", "ip.txt", "ipv6.txt", "license", "license.txt":
		return true
	default:
		return false
	}
}

func requireExtracted(dest string) error {
	if _, err := os.Stat(filepath.Join(dest, executableName())); err != nil {
		return errors.New("CFST 发行包中未找到可执行文件")
	}
	if _, err := os.Stat(filepath.Join(dest, "ip.txt")); err != nil {
		return errors.New("CFST 发行包中未找到 ip.txt")
	}
	return nil
}

type Result struct {
	IP        string
	LatencyMS string
	SpeedMB   string
	Colo      string
}

func Optimize(ctx context.Context, binaryPath, host string, output io.Writer) (Result, error) {
	if net.ParseIP(host) != nil || strings.ContainsAny(host, `/\\ \t\r\n`) || host == "" {
		return Result{}, errors.New("无效的测速域名")
	}
	ipFile := filepath.Join(filepath.Dir(binaryPath), "ip.txt")
	if _, err := os.Stat(ipFile); err != nil {
		return Result{}, fmt.Errorf("找不到 CFST IP 段文件: %w", err)
	}
	work, err := os.MkdirTemp("", "getbee-route-*")
	if err != nil {
		return Result{}, err
	}
	defer os.RemoveAll(work)
	csvPath := filepath.Join(work, "result.csv")
	args := cfstArgs(ipFile, csvPath, host)
	cmd := exec.CommandContext(ctx, binaryPath, args...)
	cmd.Dir = work
	cmd.Stdout = output
	cmd.Stderr = output
	if err := cmd.Run(); err != nil {
		return Result{}, cfstRunError(err)
	}
	result, err := parseResult(csvPath)
	if err != nil {
		return Result{}, err
	}
	if err := ValidatePinnedIP(ctx, host, result.IP); err != nil {
		return Result{}, fmt.Errorf("最快 IP 未通过 BeeAPI TLS 校验: %w", err)
	}
	return result, nil
}

func cfstRunError(err error) error {
	if errors.Is(err, os.ErrPermission) {
		return fmt.Errorf("CloudflareSpeedTest 无法从缓存目录执行；该目录可能以 noexec 挂载，请把 GETBEE_CACHE 指向允许执行的用户目录后重试: %w", err)
	}
	return fmt.Errorf("CloudflareSpeedTest 执行失败: %w", err)
}

func cfstArgs(ipFile, csvPath, host string) []string {
	return []string{
		"-f", ipFile,
		"-o", csvPath,
		"-p", "0",
		"-n", "50",
		"-t", "4",
		"-tp", "443",
		"-tlr", "0.2",
		"-url", "https://" + host + "/api/v1/public/api-endpoints",
		"-httping",
		"-httping-code", "200",
		"-dd",
	}
}

func parseResult(path string) (Result, error) {
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Result{}, errors.New("CFST 没有生成测速结果；当前网络可能阻止直连 Cloudflare IP，请稍后重试或继续使用可用域名")
		}
		return Result{}, err
	}
	defer file.Close()
	rows, err := csv.NewReader(file).ReadAll()
	if err != nil {
		return Result{}, err
	}
	if len(rows) < 2 || len(rows[1]) < 1 {
		return Result{}, errors.New("CFST 没有返回可用 IP")
	}
	row := rows[1]
	result := Result{IP: strings.TrimSpace(row[0])}
	if net.ParseIP(result.IP) == nil {
		return Result{}, errors.New("CFST 返回了无效 IP")
	}
	if len(row) > 4 {
		result.LatencyMS = strings.TrimSpace(row[4])
	}
	if len(row) > 6 {
		result.Colo = strings.TrimSpace(row[6])
		if strings.EqualFold(result.Colo, "N/A") {
			result.Colo = ""
		}
	}
	return result, nil
}

func ValidatePinnedIP(ctx context.Context, host, ip string) error {
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return errors.New("无效 IP")
	}
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, network, net.JoinHostPort(ip, "443"))
		},
		TLSHandshakeTimeout: 5 * time.Second,
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 8 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+host+"/api/v1/public/api-endpoints", nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

func HostsPath() string {
	if override := strings.TrimSpace(os.Getenv("GETBEE_HOSTS_PATH")); override != "" {
		return override
	}
	if runtime.GOOS == "windows" {
		root := os.Getenv("SystemRoot")
		if root == "" {
			root = `C:\Windows`
		}
		return filepath.Join(root, "System32", "drivers", "etc", "hosts")
	}
	return "/etc/hosts"
}

func ApplyHosts(path, host, ip string) error {
	if net.ParseIP(ip) == nil || host == "" || strings.ContainsAny(host, `/\\ \t\r\n`) {
		return errors.New("无效的 Hosts 记录")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	updated := UpdateManagedHosts(string(b), host, ip)
	if updated == string(b) {
		return nil
	}
	return writeHostsFile(path, []byte(updated))
}

func RestoreHosts(path, host string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	updated := RemoveManagedHosts(string(b), host)
	if updated == string(b) {
		return nil
	}
	return writeHostsFile(path, []byte(updated))
}

func writeHostsFile(path string, data []byte) error {
	if err := state.AtomicWrite(path, data, 0o644); err == nil {
		return nil
	}
	if err := writeInPlace(path, data); err == nil {
		return nil
	}
	if strings.TrimSpace(os.Getenv("GETBEE_HOSTS_PATH")) != "" || filepath.Clean(path) != filepath.Clean(HostsPath()) {
		return errors.New("无法写入 Hosts 文件")
	}
	var err error
	switch runtime.GOOS {
	case "linux", "darwin":
		err = writeHostsWithSudo(path, data)
	case "windows":
		err = writeHostsWithUAC(path, data)
	default:
		err = errors.New("当前系统不支持自动请求管理员权限")
	}
	if err != nil {
		return fmt.Errorf("写入 Hosts 需要管理员权限: %w", err)
	}
	written, readErr := os.ReadFile(path)
	if readErr != nil || !bytes.Equal(written, data) {
		return errors.New("管理员写入 Hosts 后校验失败")
	}
	return nil
}

func writeInPlace(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(data)
	syncErr := file.Sync()
	closeErr := file.Close()
	return errors.Join(writeErr, syncErr, closeErr)
}

func writeHostsWithSudo(path string, data []byte) error {
	sudo, err := exec.LookPath("sudo")
	if err != nil {
		return errors.New("未找到 sudo，请在管理员终端中重新运行 beeapi")
	}
	cmd := exec.Command(sudo, "tee", path)
	cmd.Stdin = bytes.NewReader(data)
	cmd.Stdout = io.Discard
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func writeHostsWithUAC(path string, data []byte) error {
	powerShell, err := exec.LookPath("powershell.exe")
	if err != nil {
		return errors.New("未找到 PowerShell，请在管理员终端中重新运行 beeapi")
	}
	data64 := base64.StdEncoding.EncodeToString(data)
	safePath := strings.ReplaceAll(path, "'", "''")
	inner := "$b=[Convert]::FromBase64String('" + data64 + "');[IO.File]::WriteAllBytes('" + safePath + "',$b)"
	encoded := encodePowerShell(inner)
	outer := "$p=Start-Process -FilePath 'powershell.exe' -Verb RunAs -ArgumentList @('-NoProfile','-NonInteractive','-EncodedCommand','" + encoded + "') -Wait -PassThru; exit $p.ExitCode"
	cmd := exec.Command(powerShell, "-NoProfile", "-NonInteractive", "-Command", outer)
	cmd.Stdout, cmd.Stderr = io.Discard, os.Stderr
	return cmd.Run()
}

func encodePowerShell(script string) string {
	runes := utf16.Encode([]rune(script))
	data := make([]byte, len(runes)*2)
	for index, value := range runes {
		binary.LittleEndian.PutUint16(data[index*2:], value)
	}
	return base64.StdEncoding.EncodeToString(data)
}

func UpdateManagedHosts(content, host, ip string) string {
	content = RemoveManagedHosts(content, host)
	content = strings.TrimRight(content, "\r\n") + "\n"
	return content + markerStart(host) + "\n" + ip + " " + host + "\n" + markerEnd(host) + "\n"
}

func RemoveManagedHosts(content, host string) string {
	start, end := markerStart(host), markerEnd(host)
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	result := make([]string, 0, len(lines))
	for index := 0; index < len(lines); index++ {
		if strings.TrimSpace(lines[index]) == start {
			endIndex := -1
			for candidate := index + 1; candidate < len(lines); candidate++ {
				if strings.TrimSpace(lines[candidate]) == end {
					endIndex = candidate
					break
				}
			}
			if endIndex >= 0 {
				index = endIndex
				continue
			}
		}
		result = append(result, lines[index])
	}
	return strings.TrimRight(strings.Join(result, "\n"), "\n") + "\n"
}

func markerStart(host string) string { return "# >>> getbeeapi managed: " + host }
func markerEnd(host string) string   { return "# <<< getbeeapi managed: " + host }
