package ipc

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"velocetorrent/internal/engine"
)

// Client is the thin IPC client used by the CLI and TUI to interact with the VeloceTorrent daemon.
type Client struct {
	baseURL    string
	httpClient *http.Client
}

type authRoundTripper struct {
	base http.RoundTripper
}

func (a *authRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if tok, _, err := LoadOrCreateAuthToken(""); err == nil && tok != "" {
		req = req.Clone(req.Context())
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("X-VT-Token", tok)
	}
	return a.base.RoundTrip(req)
}

// NewClient constructs an IPC client preferring the Unix domain socket if reachable, falling back to 127.0.0.1:7878.
func NewClient(tcpAddr string, unixPath string) *Client {
	if strings.TrimSpace(tcpAddr) == "" {
		tcpAddr = DefaultTCPAddr
	}
	if strings.TrimSpace(unixPath) == "" {
		unixPath = DefaultUnixSocketPath()
	}

	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if unixPath != "" {
				if _, err := os.Stat(unixPath); err == nil {
					if c, uErr := (&net.Dialer{Timeout: 1 * time.Second}).DialContext(ctx, "unix", unixPath); uErr == nil {
						return c, nil
					}
				}
			}
			return (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "tcp", tcpAddr)
		},
	}

	return &Client{
		baseURL: "http://" + tcpAddr,
		httpClient: &http.Client{
			Transport: &authRoundTripper{base: transport},
			Timeout:   8 * time.Second,
		},
	}
}

// Ping checks whether the VeloceTorrent daemon is alive and listening.
func (c *Client) Ping() bool {
	resp, err := c.httpClient.Get(c.baseURL + "/api/health")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// FetchState retrieves the full EngineTelemetrySnapshot from the daemon.
func (c *Client) FetchState() (*engine.EngineTelemetrySnapshot, error) {
	resp, err := c.httpClient.Get(c.baseURL + "/api/state")
	if err != nil {
		return nil, fmt.Errorf("daemon unreachable at %s: %w", c.baseURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, decodeAPIError(resp.Body, resp.StatusCode)
	}
	var snap engine.EngineTelemetrySnapshot
	if err := json.NewDecoder(resp.Body).Decode(&snap); err != nil {
		return nil, err
	}
	return &snap, nil
}

// AddTorrent queues a .torrent file path or magnet URI on the daemon.
func (c *Client) AddTorrent(source string, outputDir string, sequential bool) (*engine.TorrentTelemetrySnapshot, error) {
	return c.AddTorrentWithOptions(source, outputDir, sequential, false)
}

// AddTorrentWithOptions queues a .torrent file path or magnet URI with optional sequential or linear streaming mode.
func (c *Client) AddTorrentWithOptions(source string, outputDir string, sequential bool, stream bool) (*engine.TorrentTelemetrySnapshot, error) {
	reqPayload := AddTorrentRequest{
		Source:     source,
		OutputDir:  outputDir,
		Sequential: sequential || stream,
		Stream:     stream,
	}
	// If source is a local file that exists, also send its base64 bytes so the daemon can parse it even across working dirs
	if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(source)), "magnet:?") {
		if rawBytes, err := os.ReadFile(source); err == nil {
			reqPayload.TorrentBase64 = base64.StdEncoding.EncodeToString(rawBytes)
		}
	}

	body, err := json.Marshal(reqPayload)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Post(c.baseURL+"/api/torrents", "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("daemon unreachable: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return nil, decodeAPIError(resp.Body, resp.StatusCode)
	}

	var snap engine.TorrentTelemetrySnapshot
	if err := json.NewDecoder(resp.Body).Decode(&snap); err != nil {
		return nil, err
	}
	return &snap, nil
}

// SetFilePriority updates selective priority (skip, normal, high) for a file inside a torrent.
func (c *Client) SetFilePriority(id string, fileIndex int, prio engine.FilePriority) (*engine.TorrentTelemetrySnapshot, error) {
	body, _ := json.Marshal(FilePriorityRequest{FileIndex: fileIndex, Priority: prio})
	resp, err := c.httpClient.Post(c.baseURL+"/api/torrents/"+url.PathEscape(id)+"/files", "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, decodeAPIError(resp.Body, resp.StatusCode)
	}
	var snap engine.TorrentTelemetrySnapshot
	if err := json.NewDecoder(resp.Body).Decode(&snap); err != nil {
		return nil, err
	}
	return &snap, nil
}

// SetThrottle switches or toggles the global bandwidth scheduler profile (UNCAPPED vs BACKGROUND).
func (c *Client) SetThrottle(profile engine.ThrottleProfile, toggle bool, maxDown, maxUp int64) (*engine.ThrottleSnapshot, error) {
	body, _ := json.Marshal(ThrottleRequest{
		Profile:        profile,
		Toggle:         toggle,
		MaxDownBytesPs: maxDown,
		MaxUpBytesPs:   maxUp,
	})
	resp, err := c.httpClient.Post(c.baseURL+"/api/throttle", "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, decodeAPIError(resp.Body, resp.StatusCode)
	}
	var snap engine.ThrottleSnapshot
	if err := json.NewDecoder(resp.Body).Decode(&snap); err != nil {
		return nil, err
	}
	return &snap, nil
}

// TogglePause pauses or resumes a torrent by ID.
func (c *Client) TogglePause(id string) (*engine.TorrentTelemetrySnapshot, error) {
	return c.postTorrentAction(id, "toggle")
}

// Pause pauses a torrent by ID or hash prefix.
func (c *Client) Pause(id string) (*engine.TorrentTelemetrySnapshot, error) {
	return c.postTorrentAction(id, "pause")
}

// Stop halts swarm connections and disk writes for a torrent and sets its state to STOPPED.
func (c *Client) Stop(id string) (*engine.TorrentTelemetrySnapshot, error) {
	return c.postTorrentAction(id, "stop")
}

// Resume resumes a paused or stopped torrent by ID or hash prefix.
func (c *Client) Resume(id string) (*engine.TorrentTelemetrySnapshot, error) {
	return c.postTorrentAction(id, "resume")
}

func (c *Client) postTorrentAction(id string, action string) (*engine.TorrentTelemetrySnapshot, error) {
	resp, err := c.httpClient.Post(c.baseURL+"/api/torrents/"+url.PathEscape(id)+"/"+action, "application/json", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, decodeAPIError(resp.Body, resp.StatusCode)
	}
	var snap engine.TorrentTelemetrySnapshot
	if err := json.NewDecoder(resp.Body).Decode(&snap); err != nil {
		return nil, err
	}
	return &snap, nil
}

// ChangeTorrentDir relocates a torrent's output directory.
func (c *Client) ChangeTorrentDir(id string, newDir string) (*engine.TorrentTelemetrySnapshot, error) {
	body, _ := json.Marshal(ChangeDirRequest{OutputDir: newDir})
	resp, err := c.httpClient.Post(c.baseURL+"/api/torrents/"+url.PathEscape(id)+"/dir", "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, decodeAPIError(resp.Body, resp.StatusCode)
	}
	var snap engine.TorrentTelemetrySnapshot
	if err := json.NewDecoder(resp.Body).Decode(&snap); err != nil {
		return nil, err
	}
	return &snap, nil
}

// SetDefaultDir updates the global default output directory on the daemon.
func (c *Client) SetDefaultDir(newDir string) (*DirsResponse, error) {
	body, _ := json.Marshal(ChangeDirRequest{Path: newDir, SetDefault: true})
	resp, err := c.httpClient.Post(c.baseURL+"/api/dirs", "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, decodeAPIError(resp.Body, resp.StatusCode)
	}
	var res DirsResponse
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return nil, err
	}
	return &res, nil
}

// DeleteTorrent stops and removes a torrent session from the daemon, optionally purging downloaded files from disk.
func (c *Client) DeleteTorrent(id string, deleteFiles bool) error {
	u := fmt.Sprintf("%s/api/torrents/%s?delete_files=%v", c.baseURL, url.PathEscape(id), deleteFiles)
	req, err := http.NewRequest(http.MethodDelete, u, nil)
	if err != nil {
		return err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return decodeAPIError(resp.Body, resp.StatusCode)
	}
	return nil
}

// Shutdown requests the running daemon to flush state.db and exit cleanly.
func (c *Client) Shutdown() error {
	resp, err := c.httpClient.Post(c.baseURL+"/api/shutdown", "application/json", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return decodeAPIError(resp.Body, resp.StatusCode)
	}
	return nil
}

func decodeAPIError(r io.Reader, status int) error {
	var apiErr APIError
	if err := json.NewDecoder(r).Decode(&apiErr); err == nil && apiErr.Error != "" {
		return errors.New(apiErr.Error)
	}
	return fmt.Errorf("IPC request failed with HTTP %d", status)
}
