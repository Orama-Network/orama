package ipfs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"sync"
	"time"
)

// pinsetLookupTimeout bounds GetStored's pinset lookup. The local cluster peer
// answers from its own state in milliseconds, but holds the request while
// that state is loading.
const pinsetLookupTimeout = 3 * time.Second

// ErrNotInPinset is returned by GetStored for a CID this node does not hold
// and the cluster's pinset does not list: nothing the cluster knows of stores
// it (never pinned, or unpinned and reclaimed).
var ErrNotInPinset = errors.New("content is not in the cluster pinset")

// ErrPinsetUnavailable is returned by GetStored when, after a local miss, the
// cluster peer could not say whether the CID is pinned.
var ErrPinsetUnavailable = errors.New("the cluster pinset could not be read")

// ErrIdle is returned when a read made no progress for the idle timeout
// GetStored was given. It wraps context.DeadlineExceeded: the read ran out of
// time, which is worth retrying.
var ErrIdle = fmt.Errorf("no progress within the idle timeout: %w", context.DeadlineExceeded)

// StoredContent is content GetStored opened. Body streams it; Size is its
// length in bytes, known before the first byte is read, so a server can send
// it as Content-Length. Close releases the read.
type StoredContent struct {
	Body io.ReadCloser
	Size int64
}

// GetStored opens stored content by CID for a caller that wants to know when
// it is gone (bugboard #414) and that serves it as it arrives (bugboard #428).
// cid must be a bare CID, not an /ipfs/ path.
//
// Where it reads from: content this node pins is read from its own disk,
// offline, without consulting the cluster, so a local read does not depend on
// the cluster peer. A pin is what guarantees every block of the DAG is here;
// a node holding only some (a fetch that was cut off) would otherwise start
// serving and stop partway. Otherwise the pinset is asked before any network
// search: a CID nobody pins cannot be found by a networked fetch, which could
// only run out its time, so it is ErrNotInPinset at once.
//
// How it is read: content stored in the clear is streamed, and Size is the
// length Kubo reports (X-Content-Length), so memory does not grow with the
// object. Content sealed at rest (wrap.go) is one AES-GCM message whose tag
// ends it, so it is read whole and opened before the first byte is released,
// and Size is the plaintext's.
//
// idle, when positive, bounds every wait from the call on: for the first byte
// and between reads, not the whole transfer. A large object that keeps
// arriving is not cut off; a fetch that stalls is cancelled and its read fails
// with ErrIdle. Zero leaves ctx as the only bound.
func (c *Client) GetStored(ctx context.Context, cid, ipfsAPIURL string, idle time.Duration) (*StoredContent, error) {
	if ipfsAPIURL == "" {
		ipfsAPIURL = c.ipfsAPIURL
	}
	ctx, dog := newIdleWatchdog(ctx, idle, c.afterFunc)
	content, err := c.openStored(ctx, cid, ipfsAPIURL, dog)
	if err != nil {
		dog.stop()
		return nil, dog.explain(err)
	}
	return content, nil
}

func (c *Client) openStored(ctx context.Context, cid, ipfsAPIURL string, dog *idleWatchdog) (*StoredContent, error) {
	pinned, err := c.isPinnedLocallyAt(ctx, ipfsAPIURL, cid, "recursive")
	if err != nil {
		return nil, fmt.Errorf("local pin lookup for %s: %w", cid, err)
	}
	var body *catBody
	if pinned {
		body, err = c.cat(ctx, ipfsAPIURL, cid, true, offlineCatTimeout)
	} else {
		if err := c.requireInPinset(ctx, cid); err != nil {
			return nil, err
		}
		body, err = c.cat(ctx, ipfsAPIURL, cid, false, 0)
	}
	if err != nil {
		return nil, err
	}
	return c.storedContent(body, dog)
}

// requireInPinset returns ErrNotInPinset for a CID the pinset does not list.
func (c *Client) requireInPinset(ctx context.Context, cid string) error {
	lookupCtx, cancel := context.WithTimeout(ctx, pinsetLookupTimeout)
	inPinset, err := c.InPinset(lookupCtx, cid)
	cancel()
	if err != nil {
		if ctx.Err() != nil {
			// The caller's own deadline ran out, not the cluster peer.
			return fmt.Errorf("pinset lookup for %s: %w", cid, ctx.Err())
		}
		return fmt.Errorf("%w: %v", ErrPinsetUnavailable, err)
	}
	if !inPinset {
		return fmt.Errorf("%w: %s", ErrNotInPinset, cid)
	}
	return nil
}

// storedContent turns a cat response into StoredContent: streamed when it is
// in the clear, opened whole when it is sealed.
func (c *Client) storedContent(body *catBody, dog *idleWatchdog) (*StoredContent, error) {
	progress := &progressReader{r: body, dog: dog}
	head := make([]byte, len(wrapMagic))
	n, err := io.ReadFull(progress, head)
	head = head[:n]
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		_ = body.Close()
		return nil, err
	}
	if !hasWrapEnvelope(head) {
		if body.length < 0 {
			_ = body.Close()
			return nil, errors.New("kubo did not report the content's length (X-Content-Length)")
		}
		return &StoredContent{
			Body: &storedBody{Reader: io.MultiReader(bytes.NewReader(head), progress), body: body, dog: dog},
			Size: body.length,
		}, nil
	}
	var sealed bytes.Buffer
	if body.length > 0 {
		sealed.Grow(int(body.length))
	}
	sealed.Write(head)
	_, err = sealed.ReadFrom(progress)
	_ = body.Close()
	if err != nil {
		return nil, err
	}
	plain, err := openBlob(sealed.Bytes(), c.wrapKey)
	if err != nil {
		return nil, err
	}
	dog.stop()
	return &StoredContent{Body: io.NopCloser(bytes.NewReader(plain)), Size: int64(len(plain))}, nil
}

// storedBody is a streamed StoredContent body. A read that failed because the
// watchdog fired says so; Close ends the request and the watchdog.
type storedBody struct {
	io.Reader
	body *catBody
	dog  *idleWatchdog
}

func (b *storedBody) Read(p []byte) (int, error) {
	n, err := b.Reader.Read(p)
	if err != nil && err != io.EOF {
		err = b.dog.explain(err)
	}
	return n, err
}

func (b *storedBody) Close() error {
	b.dog.stop()
	return b.body.Close()
}

// progressReader tells the watchdog every time bytes arrive.
type progressReader struct {
	r   io.Reader
	dog *idleWatchdog
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	if n > 0 {
		p.dog.progress()
	}
	return n, err
}

// watchdogTimer is the part of *time.Timer the idle watchdog uses.
type watchdogTimer interface {
	Reset(d time.Duration) bool
	Stop() bool
}

// afterFunc starts a watchdogTimer that calls f once d has passed. The
// Client's is time.AfterFunc; a test gives its own clock, so what it checks
// does not depend on how fast the machine runs it.
type afterFunc func(d time.Duration, f func()) watchdogTimer

func wallClockAfterFunc(d time.Duration, f func()) watchdogTimer { return time.AfterFunc(d, f) }

// idleWatchdog cancels a context when no progress was reported for its idle
// timeout. A zero timeout makes it a no-op that never fires.
type idleWatchdog struct {
	idle   time.Duration
	cancel context.CancelFunc
	mu     sync.Mutex
	timer  watchdogTimer
	fired  bool
}

func newIdleWatchdog(parent context.Context, idle time.Duration, after afterFunc) (context.Context, *idleWatchdog) {
	ctx, cancel := context.WithCancel(parent)
	d := &idleWatchdog{idle: idle, cancel: cancel}
	if idle > 0 {
		d.timer = after(idle, d.fire)
	}
	return ctx, d
}

func (d *idleWatchdog) fire() {
	d.mu.Lock()
	d.fired = true
	d.mu.Unlock()
	d.cancel()
}

func (d *idleWatchdog) progress() {
	if d.timer != nil {
		d.timer.Reset(d.idle)
	}
}

// stop disarms the watchdog and releases its context.
func (d *idleWatchdog) stop() {
	if d.timer != nil {
		d.timer.Stop()
	}
	d.cancel()
}

// explain replaces err with ErrIdle when the watchdog is what ended the read.
func (d *idleWatchdog) explain(err error) error {
	d.mu.Lock()
	fired := d.fired
	d.mu.Unlock()
	if fired {
		return fmt.Errorf("%w (%s): %v", ErrIdle, d.idle, err)
	}
	return err
}

// catBody is a cat response body and the length Kubo announced for it, -1
// when it announced none.
type catBody struct {
	io.ReadCloser
	length int64
}

// cat is catOnce through the stream client, which has no whole-call timeout,
// that also returns the length Kubo announced.
func (c *Client) cat(ctx context.Context, ipfsAPIURL, cid string, offline bool, headerTimeout time.Duration) (*catBody, error) {
	body, header, err := c.catWithHeader(ctx, c.streamClient, ipfsAPIURL, cid, offline, headerTimeout)
	if err != nil {
		return nil, err
	}
	length := int64(-1)
	if v := header.Get("X-Content-Length"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n >= 0 {
			length = n
		}
	}
	return &catBody{ReadCloser: body, length: length}, nil
}
