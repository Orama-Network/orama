package ipfs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Orama-Network/orama/core/pkg/constants"
	"go.uber.org/zap"
)

// IPFSClient defines the interface for IPFS operations
type IPFSClient interface {
	Add(ctx context.Context, reader io.Reader, name string) (*AddResponse, error)
	AddLocal(ctx context.Context, reader io.Reader, name string) (*AddResponse, error)
	AddDirectory(ctx context.Context, dirPath string) (*AddResponse, error)
	Pin(ctx context.Context, cid string, name string, replicationFactor int) (*PinResponse, error)
	PinStatus(ctx context.Context, cid string) (*PinStatus, error)
	Get(ctx context.Context, cid string, ipfsAPIURL string) (io.ReadCloser, error)
	GetStored(ctx context.Context, cid string, ipfsAPIURL string, idle time.Duration) (*StoredContent, error)
	Unpin(ctx context.Context, cid string) error
	EvictLocal(ctx context.Context, cid string) (int, error)
	Health(ctx context.Context) error
	GetPeerCount(ctx context.Context) (int, error)
	Close(ctx context.Context) error
}

// Client wraps an IPFS Cluster HTTP API client for storage operations
type Client struct {
	apiURL     string
	ipfsAPIURL string
	httpClient *http.Client
	// streamClient is httpClient without its whole-call Timeout, for the
	// reads GetStored streams: their bound is the caller's idle timeout,
	// which tracks progress, so a large object is not cut off at 60 s.
	streamClient *http.Client
	logger       *zap.Logger
	wrapKey      []byte
	// afterFunc is the clock of GetStored's idle watchdog.
	afterFunc afterFunc
}

// Config holds configuration for the IPFS client
type Config struct {
	// ClusterAPIURL is the base URL for IPFS Cluster HTTP API (e.g., "http://localhost:9094")
	// If empty, defaults to "http://localhost:9094"
	ClusterAPIURL string

	// IPFSAPIURL is the base URL for IPFS daemon API (e.g., "http://localhost:10107")
	// Used for operations that require IPFS daemon directly (like directory uploads)
	IPFSAPIURL string

	// ClusterAPIPassword is the REST API's basic-auth password
	// (ClusterRESTPassword). Every request to ClusterAPIURL carries it.
	ClusterAPIPassword string

	// KuboAPIToken is the bearer Kubo's RPC requires (KuboAPIToken). Every
	// request to IPFSAPIURL carries it. It is not the cluster password.
	KuboAPIToken string

	// Timeout is the timeout for client operations
	// If zero, defaults to 60 seconds
	Timeout time.Duration

	// WrapKey is a 32-byte AES-256 key used to encrypt private blobs before
	// Add (feat-270). Empty skips wrapping (tests). UnixFS directories and
	// tarball deploys are never wrapped.
	WrapKey []byte
}

// PinStatus represents the status of a pinned CID
type PinStatus struct {
	Cid               string   `json:"cid"`
	Name              string   `json:"name"`
	Status            string   `json:"status"` // "pinned", "pinning", "queued", "unpinned", "error"
	ReplicationMin    int      `json:"replication_min"`
	ReplicationMax    int      `json:"replication_max"`
	ReplicationFactor int      `json:"replication_factor"`
	Peers             []string `json:"peers"`
	PinnedPeers       int      `json:"pinned_peers"`
	TotalPeers        int      `json:"total_peers"`
	Error             string   `json:"error,omitempty"`
}

// Aggregated PinStatus.Status values. IPFS-Cluster reports a per-peer status;
// these are the honest cluster-wide rollups PinStatus computes from the peer_map's
// allocated peers (peers reporting "remote" are not allocated and are ignored).
const (
	PinStatusPinned  = "pinned"  // every peer holds the block
	PinStatusPinning = "pinning" // at least one peer is still fetching
	PinStatusError   = "error"   // at least one peer failed to pin
	PinStatusUnknown = "unknown" // no allocated peers reported (cluster can't confirm)
)

// peerStatusRemote is the per-peer status ipfs-cluster gives a peer the pin is
// not allocated to (it is tracked elsewhere).
const peerStatusRemote = "remote"

const (
	// evictPinPropagationTimeout bounds how long an immediate eviction waits
	// for the cluster's unpin to reach this node's kubo (bugboard #153).
	// Generous enough to cover normal cross-region consensus propagation, short
	// enough that the caller's 30s per-node fan-out budget is not exhausted by
	// a node that will never converge.
	evictPinPropagationTimeout = 20 * time.Second

	// evictPinPollInterval is how often the local pinset is re-checked while
	// waiting. Short: the wait is normally sub-second and every extra tick is
	// latency the tenant sees on a privacy delete.
	evictPinPollInterval = 250 * time.Millisecond
)

// AddResponse represents the response from adding content to IPFS
type AddResponse struct {
	Name string `json:"name"`
	Cid  string `json:"cid"`
	Size int64  `json:"size"`
}

// ipfsDaemonAddResponse represents the response from IPFS daemon's /add endpoint
// The daemon returns Size as a string, unlike Cluster which returns it as int64
type ipfsDaemonAddResponse struct {
	Name string `json:"Name"`
	Hash string `json:"Hash"` // Daemon uses "Hash" instead of "Cid"
	Size string `json:"Size"` // Daemon returns size as string
}

// PinResponse represents the response from pinning a CID
type PinResponse struct {
	Cid  string `json:"cid"`
	Name string `json:"name"`
}

// NewClient creates a new IPFS Cluster client wrapper
func NewClient(cfg Config, logger *zap.Logger) (*Client, error) {
	apiURL := cfg.ClusterAPIURL
	if apiURL == "" {
		apiURL = "http://localhost:9094"
	}

	ipfsAPIURL := cfg.IPFSAPIURL
	if ipfsAPIURL == "" {
		ipfsAPIURL = constants.LocalIPFSAPIURL()
	}

	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = 60 * time.Second
	}

	httpClient := &http.Client{
		Timeout: timeout,
	}
	if cfg.ClusterAPIPassword != "" || cfg.KuboAPIToken != "" {
		transport, err := newAPIAuthTransport(apiURL, cfg.ClusterAPIPassword, ipfsAPIURL, cfg.KuboAPIToken)
		if err != nil {
			return nil, err
		}
		httpClient.Transport = transport
	}

	return &Client{
		apiURL:       apiURL,
		ipfsAPIURL:   ipfsAPIURL,
		httpClient:   httpClient,
		streamClient: &http.Client{Transport: httpClient.Transport},
		logger:       logger,
		wrapKey:      append([]byte(nil), cfg.WrapKey...),
		afterFunc:    wallClockAfterFunc,
	}, nil
}

// Health checks if the IPFS Cluster API is healthy
func (c *Client) Health(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, "GET", c.apiURL+"/id", nil)
	if err != nil {
		return fmt.Errorf("failed to create health check request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("health check request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("health check failed with status: %d", resp.StatusCode)
	}

	return nil
}

// GetPeerCount returns the number of cluster peers
func (c *Client) GetPeerCount(ctx context.Context) (int, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", c.apiURL+"/peers", nil)
	if err != nil {
		return 0, fmt.Errorf("failed to create peers request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("peers request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("peers request failed with status: %d", resp.StatusCode)
	}

	// The /peers endpoint returns NDJSON (newline-delimited JSON), not a JSON array
	// We need to stream-read each peer object
	dec := json.NewDecoder(resp.Body)
	peerCount := 0
	for {
		var peer map[string]interface{}
		err := dec.Decode(&peer)
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return 0, fmt.Errorf("failed to decode peers response: %w", err)
		}
		peerCount++
	}

	return peerCount, nil
}

// Add imports content into this node's Kubo and pins it on every cluster peer.
// A caller that pins with a replication factor of its own uses AddLocal and
// then Pin: pinning everywhere first and then narrowing it made every peer
// start fetching the content and most of them cancel and unpin it again, and
// that churn held the cluster's pin slots while a function deploy waited for
// its WASM to be pinned (stagenet e2e, 2026-10-03).
func (c *Client) Add(ctx context.Context, reader io.Reader, name string) (*AddResponse, error) {
	added, err := c.AddLocal(ctx, reader, name)
	if err != nil {
		return nil, err
	}
	// -1 is every cluster peer, which is what Cluster /add pinned.
	if _, err := c.Pin(ctx, added.Cid, name, -1); err != nil {
		return nil, fmt.Errorf("pin %s after import: %w", added.Cid, err)
	}
	return added, nil
}

// AddLocal imports content into this node's Kubo, pinned there, without
// pinning it on the cluster: the caller pins it with the replication it wants.
func (c *Client) AddLocal(ctx context.Context, reader io.Reader, name string) (*AddResponse, error) {
	// Track original size by reading into memory first
	// This allows us to return the actual byte count, not the DAG size
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("failed to read data: %w", err)
	}
	originalSize := int64(len(data))
	if wrapPrivateBlob(name) && len(c.wrapKey) == 32 {
		sealed, err := sealBlob(data, c.wrapKey)
		if err != nil {
			return nil, fmt.Errorf("encrypt before IPFS add: %w", err)
		}
		data = sealed
	}

	// Kubo builds the DAG. Cluster /add streams bytes through block/put, and
	// Kubo 0.38 can store a chunk under a different CID than the DAG links
	// while still returning 200. cat then ends with "failed to fetch all nodes".
	added, err := c.addViaKubo(ctx, data, name)
	if err != nil {
		return nil, err
	}

	added.Name = name
	added.Size = originalSize
	return added, nil
}

// addViaKubo imports data with Kubo's /api/v0/add and returns the file CID.
// pin=true keeps the blocks on this node; the caller also pins them on the cluster.
func (c *Client) addViaKubo(ctx context.Context, data []byte, name string) (*AddResponse, error) {
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	part, err := writer.CreateFormFile("file", name)
	if err != nil {
		return nil, fmt.Errorf("failed to create form file: %w", err)
	}
	if _, err := part.Write(data); err != nil {
		return nil, fmt.Errorf("failed to copy data: %w", err)
	}
	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("failed to close writer: %w", err)
	}

	apiURL := c.ipfsAPIURL + "/api/v0/add?pin=true&cid-version=0&hash=sha2-256"
	req, err := http.NewRequestWithContext(ctx, "POST", apiURL, &buf)
	if err != nil {
		return nil, fmt.Errorf("failed to create add request: %w", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("add request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("add failed with status %d: %s", resp.StatusCode, string(body))
	}

	dec := json.NewDecoder(resp.Body)
	var entries []ipfsDaemonAddResponse
	for {
		var chunk ipfsDaemonAddResponse
		if err := dec.Decode(&chunk); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, fmt.Errorf("failed to decode add response: %w", err)
		}
		entries = append(entries, chunk)
	}
	cid, err := fileCIDFromAdd(entries, name)
	if err != nil {
		return nil, err
	}
	return &AddResponse{Name: name, Cid: cid}, nil
}

// fileCIDFromAdd is the CID of the file that was imported.
//
// A name with a slash (proof/build.txt) makes Kubo return one object per
// directory and then the file. The last object is the directory, and cat of
// a directory fails. The file is the object whose name is the path we sent.
func fileCIDFromAdd(entries []ipfsDaemonAddResponse, name string) (string, error) {
	var last string
	for _, entry := range entries {
		if entry.Hash == "" {
			continue
		}
		last = entry.Hash
		if name != "" && entry.Name == name {
			return entry.Hash, nil
		}
	}
	if last == "" {
		return "", fmt.Errorf("add response missing CID")
	}
	return last, nil
}

// AddDirectory adds all files in a directory to IPFS and returns the root directory CID
// Uses IPFS daemon's multipart upload to preserve directory structure
func (c *Client) AddDirectory(ctx context.Context, dirPath string) (*AddResponse, error) {
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)

	var totalSize int64
	var fileCount int

	// Walk directory and add all files to multipart request
	err := filepath.Walk(dirPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Skip directories themselves (IPFS will create them from file paths)
		if info.IsDir() {
			return nil
		}

		// Get relative path from dirPath
		relPath, err := filepath.Rel(dirPath, path)
		if err != nil {
			return fmt.Errorf("failed to get relative path: %w", err)
		}

		// Read file
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("failed to read file %s: %w", path, err)
		}

		totalSize += int64(len(data))
		fileCount++

		// Add file to multipart with relative path
		part, err := writer.CreateFormFile("file", relPath)
		if err != nil {
			return fmt.Errorf("failed to create form file: %w", err)
		}

		if _, err := part.Write(data); err != nil {
			return fmt.Errorf("failed to write file data: %w", err)
		}

		return nil
	})

	if err != nil {
		return nil, err
	}

	if fileCount == 0 {
		return nil, fmt.Errorf("no files found in directory")
	}

	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("failed to close writer: %w", err)
	}

	// Upload to IPFS daemon (not Cluster) with wrap-in-directory
	// This creates a UnixFS directory structure
	ipfsDaemonURL := c.ipfsAPIURL + "/api/v0/add?wrap-in-directory=true"

	req, err := http.NewRequestWithContext(ctx, "POST", ipfsDaemonURL, &buf)
	if err != nil {
		return nil, fmt.Errorf("failed to create add request: %w", err)
	}

	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("add request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("add failed with status %d: %s", resp.StatusCode, string(body))
	}

	// Read NDJSON responses
	// IPFS daemon returns entries for each file and subdirectory
	// The last entry should be the root directory (or deepest subdirectory if no wrapper)
	dec := json.NewDecoder(resp.Body)
	var rootCID string
	var lastEntry ipfsDaemonAddResponse

	for {
		var chunk ipfsDaemonAddResponse
		if err := dec.Decode(&chunk); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, fmt.Errorf("failed to decode add response: %w", err)
		}
		lastEntry = chunk

		// With wrap-in-directory, the entry with empty name is the wrapper directory
		if chunk.Name == "" {
			rootCID = chunk.Hash
		}
	}

	// Use the last entry if no wrapper directory found
	if rootCID == "" {
		rootCID = lastEntry.Hash
	}

	if rootCID == "" {
		return nil, fmt.Errorf("no root CID returned from IPFS daemon")
	}

	c.logger.Debug("Directory uploaded to IPFS",
		zap.String("root_cid", rootCID),
		zap.Int("file_count", fileCount),
		zap.Int64("total_size", totalSize))

	// Pin to cluster for distribution
	_, err = c.Pin(ctx, rootCID, "", 1)
	if err != nil {
		c.logger.Warn("Failed to pin directory to cluster",
			zap.String("cid", rootCID),
			zap.Error(err))
	}

	return &AddResponse{
		Cid:  rootCID,
		Size: totalSize,
	}, nil
}

// Pin pins a CID with specified replication factor
// IPFS Cluster expects pin options (including name) as query parameters, not in JSON body
func (c *Client) Pin(ctx context.Context, cid string, name string, replicationFactor int) (*PinResponse, error) {
	// Build URL with query parameters
	reqURL := c.apiURL + "/pins/" + cid
	values := url.Values{}
	values.Set("replication-min", fmt.Sprintf("%d", replicationFactor))
	values.Set("replication-max", fmt.Sprintf("%d", replicationFactor))
	if name != "" {
		values.Set("name", name)
	}
	if len(values) > 0 {
		reqURL += "?" + values.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, "POST", reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create pin request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("pin request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("pin failed with status %d: %s", resp.StatusCode, string(body))
	}

	var result PinResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode pin response: %w", err)
	}

	// If IPFS Cluster doesn't return the name in the response, use the one from the request
	if result.Name == "" && name != "" {
		result.Name = name
	}
	// Ensure CID is set
	if result.Cid == "" {
		result.Cid = cid
	}

	return &result, nil
}

// PinStatus retrieves the status of a pinned CID
func (c *Client) PinStatus(ctx context.Context, cid string) (*PinStatus, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", c.apiURL+"/pins/"+cid, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create pin status request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("pin status request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("pin not found: %s", cid)
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("pin status failed with status %d: %s", resp.StatusCode, string(body))
	}

	// IPFS Cluster returns GlobalPinInfo, we need to map it to our PinStatus
	var gpi struct {
		Cid     string `json:"cid"`
		Name    string `json:"name"`
		PeerMap map[string]struct {
			Status interface{} `json:"status"` // TrackerStatus can be string or int
			Error  string      `json:"error,omitempty"`
		} `json:"peer_map"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&gpi); err != nil {
		return nil, fmt.Errorf("failed to decode pin status response: %w", err)
	}

	// Use name from GlobalPinInfo
	name := gpi.Name

	// Aggregate the per-peer statuses HONESTLY. IPFS-Cluster's POST /pins
	// returns before the async per-peer pinning finishes, so a CID can be
	// "pinning"/"remote"/errored on some peers while our caller needs to know
	// whether it is DURABLE everywhere before serving invokes (bugboard #137).
	// We must never optimistically report "pinned" when we can't confirm it.
	peers := make([]string, 0, len(gpi.PeerMap))
	pinnedPeers := 0
	var errorMsg string
	var firstNonPinned string
	anyError := false
	anyPinning := false
	for peerID, pinInfo := range gpi.PeerMap {
		s := normalizePeerStatus(pinInfo.Status)
		if s == peerStatusRemote {
			// peer_map lists every cluster peer; "remote" is a peer the pin is
			// not allocated to, so it holds no replica and says nothing about
			// the pin's health. Only the allocated peers decide the status.
			continue
		}
		peers = append(peers, peerID)
		switch {
		case s == PinStatusPinned:
			pinnedPeers++
		case isErrorStatus(s):
			anyError = true
		case isPinningStatus(s):
			anyPinning = true
		}
		if s != PinStatusPinned && firstNonPinned == "" {
			firstNonPinned = s
		}
		if pinInfo.Error != "" {
			errorMsg = pinInfo.Error
		}
	}

	status := aggregatePinStatus(len(peers), pinnedPeers, anyError, anyPinning, firstNonPinned)

	result := &PinStatus{
		Cid:               gpi.Cid,
		Name:              name,
		Status:            status,
		ReplicationMin:    0, // Not available in GlobalPinInfo
		ReplicationMax:    0, // Not available in GlobalPinInfo
		ReplicationFactor: len(peers),
		Peers:             peers,
		PinnedPeers:       pinnedPeers,
		TotalPeers:        len(peers),
		Error:             errorMsg,
	}

	// Ensure CID is set
	if result.Cid == "" {
		result.Cid = cid
	}

	return result, nil
}

// normalizePeerStatus renders a per-peer TrackerStatus (which the cluster API
// encodes as either a string or a number) to a lowercase string.
func normalizePeerStatus(raw interface{}) string {
	if raw == nil {
		return ""
	}
	if s, ok := raw.(string); ok {
		return strings.ToLower(strings.TrimSpace(s))
	}
	return strings.ToLower(fmt.Sprintf("%v", raw))
}

// isErrorStatus reports whether a per-peer status represents a pin failure.
func isErrorStatus(s string) bool {
	switch s {
	case "pin_error", "cluster_error":
		return true
	}
	return strings.Contains(s, "error")
}

// isPinningStatus reports whether a per-peer status is an in-progress pin.
func isPinningStatus(s string) bool {
	switch s {
	case "pinning", "pin_queued", "queued":
		return true
	}
	return false
}

// aggregatePinStatus rolls the per-peer tallies into a single honest cluster
// status. "pinned" requires EVERY peer pinned; errors win over in-progress; an
// peer_map with no allocated peer is "unknown" (the cluster couldn't confirm
// anything).
func aggregatePinStatus(totalPeers, pinnedPeers int, anyError, anyPinning bool, firstNonPinned string) string {
	switch {
	case totalPeers == 0:
		return PinStatusUnknown
	case pinnedPeers == totalPeers:
		return PinStatusPinned
	case anyError:
		return PinStatusError
	case anyPinning:
		return PinStatusPinning
	default:
		return firstNonPinned
	}
}

// Unpin removes a pin from a CID
func (c *Client) Unpin(ctx context.Context, cid string) error {
	req, err := http.NewRequestWithContext(ctx, "DELETE", c.apiURL+"/pins/"+cid, nil)
	if err != nil {
		return fmt.Errorf("failed to create unpin request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("unpin request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("unpin failed with status %d: %s", resp.StatusCode, string(body))
	}

	return nil
}

// clusterNotInPinset is how IPFS Cluster (v1.1.6) words the 404 for a CID its
// pinset does not hold: {"code":404,"message":"pin is not part of the pinset"},
// seen live on devnet. Only that answer means "absent"; any other 404 is a
// failure, so a state error is never read as "gone".
const clusterNotInPinset = "not part of the pinset"

// InPinset reports whether cid is in the pinset, i.e. whether any node is
// meant to hold it. It asks the local cluster peer (GET /allocations/<cid>),
// which answers from its own copy of the CRDT state: no per-peer status
// aggregation and no network search for the content. That copy can lag a
// fresh pin by seconds, and a peer whose state is still loading holds the
// request until it is ready, so callers bound it with their own deadline.
//
// A CID that is not in the pinset is not stored anywhere the cluster knows
// of — never pinned, or unpinned and since reclaimed — and a networked cat
// for it can only run out its deadline (bugboard #414).
func (c *Client) InPinset(ctx context.Context, cid string) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.apiURL+"/allocations/"+url.PathEscape(cid), nil)
	if err != nil {
		return false, fmt.Errorf("failed to create allocations request for %s: %w", cid, err)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return false, fmt.Errorf("allocations request for %s to IPFS Cluster at %s failed: %w", cid, c.apiURL, err)
	}
	defer resp.Body.Close()

	// Read (and so drained, letting the connection be reused) either way.
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
	switch {
	case resp.StatusCode == http.StatusOK:
		return true, nil
	case resp.StatusCode == http.StatusNotFound && strings.Contains(string(body), clusterNotInPinset):
		return false, nil
	}
	return false, fmt.Errorf("allocations for %s failed with status %d: %s", cid, resp.StatusCode, string(body))
}

// Get retrieves content from IPFS by CID
// Note: This uses the IPFS HTTP API, not the Cluster API
func (c *Client) Get(ctx context.Context, cid string, ipfsAPIURL string) (io.ReadCloser, error) {
	// Use the client's configured IPFS API URL if not provided
	if ipfsAPIURL == "" {
		ipfsAPIURL = c.ipfsAPIURL
	}

	// Offline first.
	//
	// Content this node has pinned is on its own disk, and `cat` without
	// offline=true still consults the DHT and bitswap before answering — so a
	// local read could sit behind a network fetch for the caller's whole
	// budget. That is the mechanism behind the cold-fetch stalls in bug-167.
	// dagBlocks already does this correctly; Get did not.
	if r, err := c.getLocal(ctx, ipfsAPIURL, cid); !isContentNotFound(err) {
		// Served locally, or a local read that failed for any reason OTHER
		// than "we do not have it": a real failure of this node, which
		// retrying over the network would hide.
		return r, err
	}
	return c.getNetworked(ctx, ipfsAPIURL, cid)
}

// getLocal reads cid from this node's blocks only. A miss — at the root, or
// partway through the DAG — is errContentNotFound.
func (c *Client) getLocal(ctx context.Context, ipfsAPIURL, cid string) (io.ReadCloser, error) {
	body, err := c.catOnce(ctx, ipfsAPIURL, cid, true, offlineCatTimeout)
	if err != nil {
		return nil, err
	}
	return c.unwrapGet(body)
}

// getNetworked fetches cid through Kubo's normal path, peers included, on the
// caller's budget.
func (c *Client) getNetworked(ctx context.Context, ipfsAPIURL, cid string) (io.ReadCloser, error) {
	body, err := c.catOnce(ctx, ipfsAPIURL, cid, false, 0)
	if err != nil {
		return nil, err
	}
	return c.unwrapGet(body)
}

func (c *Client) unwrapGet(body io.ReadCloser) (io.ReadCloser, error) {
	data, err := io.ReadAll(body)
	_ = body.Close()
	if err != nil {
		return nil, err
	}
	plain, err := openBlob(data, c.wrapKey)
	if err != nil {
		return nil, err
	}
	return bufferedContent{bytes.NewReader(plain)}, nil
}

// bufferedContent is an object read in full. Len is its size; callers of Get
// may rely on it.
type bufferedContent struct{ *bytes.Reader }

func (bufferedContent) Close() error { return nil }

// maxErrorBodyBytes bounds how much of an error response is read into an
// error message.
const maxErrorBodyBytes = 4 << 10

// offlineCatTimeout bounds how long the local-only attempt waits for Kubo to
// answer. It reads from disk, so this is generous; its purpose is to stop a
// wedged daemon eating the whole budget before the networked attempt gets a
// turn. It does not bound reading the object.
const offlineCatTimeout = 2 * time.Second

// errContentNotFound marks a CID this node does not hold, which is the one
// failure that justifies falling through to a networked fetch.
var errContentNotFound = errors.New("content not found")

func isContentNotFound(err error) bool { return errors.Is(err, errContentNotFound) }

// ErrStreamFailed marks a fetch Kubo started and could not finish: the blocks
// were not reachable from any peer it asked ("failed to fetch all nodes").
var ErrStreamFailed = errors.New("kubo stream failed")

// IsContentUnavailable reports whether err means the content is not (yet)
// retrievable from this node or its peers, as opposed to a failure of the
// node itself. A caller that knows the content was just pinned may wait for it;
// any other error waiting cannot fix.
func IsContentUnavailable(err error) bool {
	return errors.Is(err, errContentNotFound) || errors.Is(err, ErrStreamFailed)
}

// kuboLocalMiss is how Kubo words an offline `cat` of a block it does not
// hold. It answers such a request with HTTP 500 and this message, never 404:
// verified against Kubo 0.43.1 (bugboard #414). Recognising only a 404 meant a
// node that did not hold an object never fetched it from its peers.
const kuboLocalMiss = "not found locally"

// catOnce performs one `cat`, optionally restricted to local blocks.
//
// headerTimeout, when set, bounds only the wait for Kubo's response headers:
// the time to find the first block. Reading the body is bounded by ctx alone,
// so a large object this node does hold is not cut off by a deadline meant to
// stop a wedged daemon.
func (c *Client) catOnce(ctx context.Context, ipfsAPIURL, cid string, offline bool, headerTimeout time.Duration) (io.ReadCloser, error) {
	body, _, err := c.catWithHeader(ctx, c.httpClient, ipfsAPIURL, cid, offline, headerTimeout)
	return body, err
}

// catWithHeader is catOnce through client, that also returns the response's
// header.
func (c *Client) catWithHeader(ctx context.Context, client *http.Client, ipfsAPIURL, cid string, offline bool, headerTimeout time.Duration) (io.ReadCloser, http.Header, error) {
	// The response body outlives this function, so a deferred cancel would
	// close the stream the caller is about to read. Cancel on every failure
	// path, and hand ownership to the body on success.
	ctx, cancel := context.WithCancel(ctx)
	var headerTimer *time.Timer
	if headerTimeout > 0 {
		headerTimer = time.AfterFunc(headerTimeout, cancel)
	}

	endpoint := fmt.Sprintf("%s/api/v0/cat?arg=%s", ipfsAPIURL, url.QueryEscape(cid))
	if offline {
		endpoint += "&offline=true"
	}

	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, nil)
	if err != nil {
		cancel()
		return nil, nil, fmt.Errorf("failed to create get request: %w", err)
	}

	resp, err := client.Do(req)
	if headerTimer != nil && !headerTimer.Stop() {
		// The timer fired: whether Do failed on it or the headers arrived just
		// as it did, this is Kubo not answering in time, not a cancellation.
		if err == nil {
			resp.Body.Close()
		}
		err = fmt.Errorf("no response within %s: %w", headerTimeout, context.DeadlineExceeded)
	}
	if err != nil {
		cancel()
		return nil, nil, fmt.Errorf("get %s from %s (offline=%v): %w", cid, ipfsAPIURL, offline, err)
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
		resp.Body.Close()
		cancel()
		if resp.StatusCode == http.StatusNotFound || (offline && strings.Contains(string(body), kuboLocalMiss)) {
			return nil, nil, fmt.Errorf("%w (CID: %s, node %s, offline=%v)", errContentNotFound, cid, ipfsAPIURL, offline)
		}
		return nil, nil, fmt.Errorf("get %s from %s failed with status %d: %s", cid, ipfsAPIURL, resp.StatusCode, string(body))
	}

	return &cancelOnCloseBody{ReadCloser: &streamErrorBody{resp: resp, offline: offline}, cancel: cancel}, resp.Header, nil
}

// streamErrorBody surfaces an error Kubo reports after the body has started.
// `cat` streams: Kubo sends 200 and the first bytes, and a failure later in
// the DAG arrives only in the X-Stream-Error trailer, after which the body
// simply ends. Read as a clean EOF, that was a truncated object served as a
// success. An offline read that stopped on a block this node lacks is a miss.
type streamErrorBody struct {
	resp    *http.Response
	offline bool
}

func (b *streamErrorBody) Read(p []byte) (int, error) {
	n, err := b.resp.Body.Read(p)
	if err != io.EOF {
		return n, err
	}
	msg := b.resp.Trailer.Get("X-Stream-Error")
	switch {
	case msg == "":
		return n, err
	case b.offline && strings.Contains(msg, kuboLocalMiss):
		return n, fmt.Errorf("%w partway through the DAG: %s", errContentNotFound, msg)
	default:
		return n, fmt.Errorf("%w: %s", ErrStreamFailed, msg)
	}
}

func (b *streamErrorBody) Close() error { return b.resp.Body.Close() }

// cancelOnCloseBody releases the request context when the caller is done with
// the stream, rather than when the function that opened it returned.
type cancelOnCloseBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b *cancelOnCloseBody) Close() error {
	err := b.ReadCloser.Close()
	b.cancel()
	return err
}

// EvictLocal immediately reclaims the blocks of a CID from THIS node's local
// kubo blockstore, so an unpinned CID becomes unfetchable without waiting for
// the scheduled `ipfs repo gc` sweep (bugboard #153 — the ~6h privacy window).
//
// It walks the CID's DAG via `refs -r` and `block rm`s each block (plus the
// root) WITHOUT --force. block rm without force is pin-safe: kubo refuses to
// remove a block still part of any pinned DAG, so a block shared with another
// still-pinned CID is left intact — defence in depth alongside the caller's
// cross-namespace zero-pin gate. Uses c.ipfsAPIURL (the kubo daemon API), NOT
// the cluster API.
//
// Best-effort and idempotent: a block already absent is a no-op success; a
// block still locally pinned (cluster unpin not yet propagated to this node) is
// surfaced in the joined error so the caller can retry. Returns blocks removed.
func (c *Client) EvictLocal(ctx context.Context, cid string) (int, error) {
	// Wait for THIS node's kubo to drop its own pin first.
	//
	// IPFS-Cluster's unpin returns as soon as the removal is committed to its
	// consensus log; each peer's kubo then unpins asynchronously. The eviction
	// fan-out fires immediately after that return, so on every node except the
	// one that served the request the pin is typically still in place — and
	// block rm without --force correctly refuses to touch a pinned block. The
	// result was a permanent "partial" on a healthy cluster, with the blocks
	// surviving until the next GC sweep: exactly the window this is meant to
	// close. Polling the real signal (is it still pinned here?) rather than
	// sleeping a guessed interval is what makes the reclaim deterministic.
	if err := c.waitForLocalUnpin(ctx, cid); err != nil {
		return 0, err
	}

	blocks, err := c.dagBlocks(ctx, cid)
	if err != nil {
		// This node simply does not hold the DAG. With a replication factor
		// below the cluster size that is the ordinary case for most nodes, not
		// a failure: there is nothing here to reclaim, and reporting it as an
		// error would mark the cluster-wide eviction incomplete forever on any
		// cluster larger than RF.
		if isNotFoundLocally(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("enumerate blocks for %s: %w", cid, err)
	}
	// refs -r returns descendants only; the root block must be removed too.
	blocks = append(blocks, cid)

	removed := 0
	var errs []error
	for _, block := range blocks {
		if err := c.blockRm(ctx, block); err != nil {
			errs = append(errs, err)
			continue
		}
		removed++
	}
	return removed, errors.Join(errs...)
}

// dagBlocks returns the unique recursive refs (DAG descendants) of a CID from
// the local kubo daemon.
func (c *Client) dagBlocks(ctx context.Context, cid string) ([]string, error) {
	q := url.Values{}
	q.Set("arg", cid)
	q.Set("recursive", "true")
	q.Set("unique", "true")
	// Local-only traversal. Without this kubo treats a missing block as a cache
	// miss and goes to the network for it, so enumerating a DAG this node does
	// not have blocks until the caller's deadline expires — turning "nothing to
	// evict here" into a 30-second stall that fails the whole fan-out. Offline
	// mode answers in milliseconds with a not-found error instead.
	q.Set("offline", "true")
	req, err := http.NewRequestWithContext(ctx, "POST", c.ipfsAPIURL+"/api/v0/refs?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("refs failed with status %d: %s", resp.StatusCode, string(body))
	}
	var refs []string
	dec := json.NewDecoder(resp.Body)
	for dec.More() {
		var line struct {
			Ref string `json:"Ref"`
			Err string `json:"Err"`
		}
		if err := dec.Decode(&line); err != nil {
			return nil, fmt.Errorf("decode refs stream: %w", err)
		}
		if line.Err != "" {
			return nil, fmt.Errorf("refs traversal error: %s", line.Err)
		}
		if line.Ref != "" {
			refs = append(refs, line.Ref)
		}
	}
	return refs, nil
}

// blockRm removes a single block from the local kubo blockstore WITHOUT force.
// An absent block is an idempotent success; a still-pinned block is surfaced as
// an error (retryable once cluster unpin has propagated to this node).
func (c *Client) blockRm(ctx context.Context, block string) error {
	q := url.Values{}
	q.Set("arg", block)
	// force stays false — pin-safety relies on kubo refusing pinned blocks.
	req, err := http.NewRequestWithContext(ctx, "POST", c.ipfsAPIURL+"/api/v0/block/rm?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("block rm %s failed with status %d: %s", block, resp.StatusCode, string(body))
	}
	// block/rm streams one JSON object per arg: {"Hash":"...","Error":"..."}.
	dec := json.NewDecoder(resp.Body)
	for dec.More() {
		var line struct {
			Hash  string `json:"Hash"`
			Error string `json:"Error"`
		}
		if err := dec.Decode(&line); err != nil {
			return fmt.Errorf("decode block rm stream: %w", err)
		}
		if line.Error == "" {
			continue
		}
		low := strings.ToLower(line.Error)
		// Already gone → reclaim goal already met (idempotent success).
		if strings.Contains(low, "not found") || strings.Contains(low, "not exist") || strings.Contains(low, "no such") {
			continue
		}
		return fmt.Errorf("block rm %s: %s", block, line.Error)
	}
	return nil
}

// waitForLocalUnpin blocks until this node's kubo no longer holds a pin on cid,
// or the bound elapses. Returns nil the moment the CID is unpinned here.
//
// A CID that is still pinned after the bound is reported as an error rather
// than pushed through: block rm would refuse anyway, and claiming a reclaim
// that did not happen is the failure mode this whole feature exists to avoid.
func (c *Client) waitForLocalUnpin(ctx context.Context, cid string) error {
	ctx, cancel := context.WithTimeout(ctx, evictPinPropagationTimeout)
	defer cancel()

	ticker := time.NewTicker(evictPinPollInterval)
	defer ticker.Stop()

	for {
		pinned, err := c.isPinnedLocally(ctx, cid)
		if err != nil {
			return fmt.Errorf("check local pin for %s: %w", cid, err)
		}
		if !pinned {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("cluster unpin of %s has not reached this node's kubo within %s (still pinned locally)",
				cid, evictPinPropagationTimeout)
		case <-ticker.C:
		}
	}
}

// isPinnedLocally reports whether this node's kubo holds a pin on cid. kubo
// answers an unpinned CID with an error object rather than an empty set, so the
// "not pinned" response is a success for our purposes.
func (c *Client) isPinnedLocally(ctx context.Context, cid string) (bool, error) {
	return c.isPinnedLocallyAt(ctx, c.ipfsAPIURL, cid, "all")
}

// isPinnedLocallyAt is isPinnedLocally against the Kubo API at ipfsAPIURL, for
// pins of pinType (Kubo's all, recursive, direct or indirect).
func (c *Client) isPinnedLocallyAt(ctx context.Context, ipfsAPIURL, cid, pinType string) (bool, error) {
	q := url.Values{}
	q.Set("arg", cid)
	q.Set("type", pinType)
	req, err := http.NewRequestWithContext(ctx, "POST", ipfsAPIURL+"/api/v0/pin/ls?"+q.Encode(), nil)
	if err != nil {
		return false, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	// kubo returns 500 with {"Message":"path '<cid>' is not pinned"} for an
	// unpinned CID. That is the answer we are waiting for, not a failure.
	if strings.Contains(strings.ToLower(string(body)), "is not pinned") {
		return false, nil
	}
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("pin ls %s failed with status %d: %s", cid, resp.StatusCode, string(body))
	}
	var out struct {
		Keys map[string]struct {
			Type string `json:"Type"`
		} `json:"Keys"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return false, fmt.Errorf("decode pin ls for %s: %w", cid, err)
	}
	_, ok := out.Keys[cid]
	return ok, nil
}

// isNotFoundLocally reports whether a kubo error means "this node does not have
// the block", as opposed to a real failure. In offline mode kubo answers a
// missing block with "block was not found locally (offline)" wrapping an
// "ipld: could not find <cid>"; both phrasings are matched so the check does not
// hinge on one exact string.
func isNotFoundLocally(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "not found locally") ||
		strings.Contains(msg, "could not find") ||
		strings.Contains(msg, "merkledag: not found")
}

// Close closes the IPFS client connection
func (c *Client) Close(ctx context.Context) error {
	// HTTP client doesn't need explicit closing
	return nil
}
