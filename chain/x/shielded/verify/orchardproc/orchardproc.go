// Package orchardproc is the second verifier of x/shielded (C12a): the same upstream orchard
// 0.15.5 checks, run by a separately built and separately pinned Rust binary
// (chain/x/shielded/orchardverifier-bin) that the node keeps running out of process.
//
// What the split gives: a memory-corruption bug, a link problem or a hang in one build cannot
// silently pass a bundle in the other, and a bug in one bundle reader (the library has its own,
// this binary uses zcash_primitives') shows as a disagreement. What it does not give: independence
// of the verification logic. Both builds check with one implementation (the same upstream crate and
// a near copy of the same wrapper), so a logic bug in either is accepted by both. See docs/whitepaper/technical-reference/vol2/43-the-shielded-pool.md.
//
// Protocol (every frame length-prefixed, u32 big-endian):
//
//	request  = u64 BE id || sighash (32) || bundle
//	response = u64 BE id || result code (1)
//	ready    = a response with id ReadyID and code 0, sent once after the verifying key is built
//
// The process is started on first use and kept. Requests are serialized. A request that times
// out, or whose process dies or answers out of protocol, kills the process and fails with
// verify.ErrVerifierFault; the next request starts a fresh process. Fail closed: a fault is
// never turned into an accept and this package never retries a request.
//
// A fault is a fact about this machine, not a verdict on the bundle, so the caller must not turn
// it into a transaction result. In a block the keeper stops the node instead (x/shielded/keeper
// VerifierHalt) and the block is executed again after the restart. The request timeout therefore
// never decides a bundle: it only turns a hung child into that stop. What the child says about a
// bundle (an accept, a rejection, a caught panic) depends on the bundle's bytes alone.
package orchardproc

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/Orama-Network/orama/chain/x/shielded/bundle"
	"github.com/Orama-Network/orama/chain/x/shielded/verify"
	"github.com/Orama-Network/orama/chain/x/shielded/verify/orchard"
)

const (
	// ReadyID is the id of the ready response.
	ReadyID = ^uint64(0)

	// DefaultRequestTimeout is the hard limit for one verification, from the first byte written
	// to the last byte read. Verifying takes milliseconds; this is a hang detector, generous
	// enough that a loaded node does not trip it.
	DefaultRequestTimeout = 30 * time.Second
	// DefaultStartTimeout bounds the start-up: the process builds the Halo 2 verifying key
	// before it says it is ready.
	DefaultStartTimeout = 60 * time.Second

	// The result codes, equal to the in-process verifier's.
	codeOK                = 0
	codeMalformed         = 1
	codeBadProofLength    = 2
	codeProofRejected     = 3
	codeSignatureRejected = 4
	codePanic             = 5

	// maxBinaryBytes bounds the verifier binary the pin check reads.
	maxBinaryBytes = 512 << 20

	idLen       = 8
	responseLen = idLen + 1
)

// Config configures a Verifier.
type Config struct {
	// Path is the verifier binary.
	Path string
	// ChainID is the chain the sighash is bound to.
	ChainID string
	// SHA256 is the hex SHA-256 the binary must have. The verifier refuses to start a file with
	// another hash, and refuses to start at all without a pin: two nodes must not disagree about a
	// bundle because one has a different binary. The node takes it from the value linked into
	// oramad at build time (chain/app ShieldedVerifierSHA256), never from an operator setting.
	SHA256 string
	// Env is the child's whole environment. It is empty by default: the verifier needs nothing from
	// the node's environment, and must not inherit its secrets. Tests that speak through a variable
	// set it here.
	Env []string
	// RequestTimeout, StartTimeout and BusyWait default to the package constants when zero.
	RequestTimeout time.Duration
	StartTimeout   time.Duration
	BusyWait       time.Duration
}

// Verifier is a verify.Verifier backed by the out-of-process binary. It is safe for concurrent
// use; requests are serialized.
type Verifier struct {
	cfg Config

	mu     *timedMutex
	proc   *process
	nextID uint64
}

var _ verify.Verifier = (*Verifier)(nil)

// VerifierID is this verifier's identity for verify.Check's distinctness rule. It differs from the
// in-process library's, so the two count as two verifiers.
const VerifierID = "orchard-process"

// ID implements verify.Verifier.
func (*Verifier) ID() string { return VerifierID }

// New returns a Verifier. It does not start the process; the first Verify (or Warm) does.
func New(cfg Config) (*Verifier, error) {
	if cfg.ChainID == "" {
		return nil, verify.ErrEmptyChainID
	}
	if cfg.RequestTimeout <= 0 {
		cfg.RequestTimeout = DefaultRequestTimeout
	}
	if cfg.StartTimeout <= 0 {
		cfg.StartTimeout = DefaultStartTimeout
	}
	if cfg.BusyWait <= 0 {
		cfg.BusyWait = DefaultBusyWait
	}
	return &Verifier{cfg: cfg, mu: newTimedMutex()}, nil
}

// Warm starts the process and waits for its ready frame, so the verifying key is built before the
// first bundle. A missing binary, a bad pin or a process that will not start is an error.
func (v *Verifier) Warm() error {
	v.mu.Lock()
	defer v.mu.Unlock()
	_, err := v.running()
	return err
}

// Close stops the process. The Verifier can still be used; it starts a new one.
func (v *Verifier) Close() error {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.stop()
	return nil
}

// ErrNotRunning is what VerifyRunning answers when there is no live process to ask: it never
// starts one.
var ErrNotRunning = errors.New("the out-of-process verifier is not running")

// ErrBusy is what VerifyRunning answers when another request or a start holds the verifier for
// longer than BusyWait. The process may be perfectly alive: the caller must not treat it as gone.
var ErrBusy = errors.New("the out-of-process verifier is busy")

// DefaultBusyWait is how long VerifyRunning waits for a request in flight before it answers ErrBusy.
const DefaultBusyWait = 250 * time.Millisecond

// Verify accepts a bundle only when the binary says every check passed. It starts the process when
// there is none, which builds the Halo 2 verifying key: seconds of work.
func (v *Verifier) Verify(b, binding []byte) error {
	sighash, err := v.prepare(b, binding)
	if err != nil {
		return err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	p, err := v.running()
	if err != nil {
		return err
	}
	return v.ask(p, sighash[:], b, v.cfg.RequestTimeout)
}

// VerifyRunning is Verify for a caller that cannot afford to wait for a start (the mempool, which
// runs under CometBFT's ABCI lock): it never starts a process. When there is none, or it has
// exited, it answers ErrNotRunning without asking anything, and the caller starts the process
// elsewhere (Warm). A request in flight (a gRPC simulation runs outside the ABCI lock) or a start
// under way is waited for at most Config.BusyWait; if the verifier is still held it answers
// ErrBusy, which says nothing about whether the process is alive.
func (v *Verifier) VerifyRunning(b, binding []byte) error {
	return v.VerifyRunningWithin(b, binding, v.cfg.RequestTimeout+v.cfg.BusyWait)
}

// minRequest is the least time a request is worth asking for: below it the answer cannot be
// expected, so a caller whose deadline is that near is told ErrBusy without the process being asked.
const minRequest = 250 * time.Millisecond

// VerifyRunningWithin is VerifyRunning for a caller with a deadline: the caller is answered within
// d. The wait for the verifier is at most BusyWait, cut to d. A caller whose deadline is nearer than
// minRequest (or than RequestTimeout, if that is smaller) is answered ErrBusy at once and the process
// is not asked. A request the process is given runs for the process's own RequestTimeout whatever d
// is: a caller whose deadline falls first is answered ErrBusy and leaves the request running, which
// keeps the verifier busy until it ends. Only exceeding RequestTimeout is a fault that stops the
// process; a caller's deadline never is, so a healthy process is never killed (and made to build its
// verifying key again) for being asked too late.
func (v *Verifier) VerifyRunningWithin(b, binding []byte, d time.Duration) error {
	sighash, err := v.prepare(b, binding)
	if err != nil {
		return err
	}
	floor := min(v.cfg.RequestTimeout, minRequest)
	if d < floor {
		return ErrBusy
	}
	start := time.Now()
	if !v.mu.LockWithin(min(v.cfg.BusyWait, d)) {
		return ErrBusy
	}
	if v.proc == nil || v.proc.exited() {
		v.mu.Unlock()
		return ErrNotRunning
	}
	left := d - time.Since(start)
	if left < floor {
		v.mu.Unlock()
		return ErrBusy
	}
	if left >= v.cfg.RequestTimeout {
		defer v.mu.Unlock()
		return v.ask(v.proc, sighash[:], b, v.cfg.RequestTimeout)
	}
	result := make(chan error, 1)
	p := v.proc
	go func() {
		defer v.mu.Unlock()
		result <- v.ask(p, sighash[:], b, v.cfg.RequestTimeout)
	}()
	t := time.NewTimer(left)
	defer t.Stop()
	select {
	case err := <-result:
		return err
	case <-t.C:
		return ErrBusy
	}
}

// prepare checks the bundle's size and computes the sighash the child is handed.
func (v *Verifier) prepare(b, binding []byte) ([32]byte, error) {
	if len(b) == 0 || len(b) > bundle.MaxBytes {
		return [32]byte{}, fmt.Errorf("%w: %d bytes", verify.ErrMalformed, len(b))
	}
	return orchard.Sighash(v.cfg.ChainID, binding, b)
}

// ask puts one request to p. The caller holds v.mu.
func (v *Verifier) ask(p *process, sighash, b []byte, timeout time.Duration) error {
	v.nextID++
	id := v.nextID
	code, err := p.exchange(id, sighash, b, timeout)
	if err != nil {
		v.stop()
		return fmt.Errorf("%w: out-of-process verifier: %w", verify.ErrVerifierFault, err)
	}
	if code == codePanic {
		// A verifier that panicked is not trusted with the next request: the child exits after
		// answering (orchardverifier-bin main.rs) and this side stops it, so the next bundle is
		// asked of a fresh process and a panic can never leave state behind that changes a later
		// verdict on this node alone.
		v.stop()
	}
	return codeToError(code)
}

func codeToError(code byte) error {
	switch code {
	case codeOK:
		return nil
	case codeMalformed:
		return verify.ErrMalformed
	case codeBadProofLength:
		return verify.ErrProofLength
	case codeProofRejected:
		return verify.ErrProofRejected
	case codeSignatureRejected:
		return verify.ErrSignatureRejected
	case codePanic:
		return verify.ErrVerifierPanic
	default:
		return fmt.Errorf("%w: out-of-process verifier result code %d", verify.ErrVerifierFault, code)
	}
}

// running returns the live process, starting one when there is none or the last one exited.
func (v *Verifier) running() (*process, error) {
	if v.proc != nil && !v.proc.exited() {
		return v.proc, nil
	}
	v.stop()
	p, err := start(v.cfg)
	if err != nil {
		return nil, err
	}
	v.proc = p
	return p, nil
}

func (v *Verifier) stop() {
	if v.proc != nil {
		v.proc.kill()
		v.proc = nil
	}
}

type process struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	done   chan struct{}
}

func start(cfg Config) (*process, error) {
	if cfg.Path == "" {
		return nil, fmt.Errorf("%w: no out-of-process verifier binary configured (flag --shielded-verifier)", verify.ErrVerifierNotLinked)
	}
	bin, err := openPinned(cfg)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", verify.ErrVerifierFault, err)
	}
	defer bin.Close()
	// The command is built by hand so a path without a slash is not looked up on PATH.
	cmd := &exec.Cmd{Path: cfg.Path, Args: []string{cfg.Path}, Env: append([]string{}, cfg.Env...)}
	bin.configure(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("%w: verifier stdin: %w", verify.ErrVerifierFault, err)
	}
	// The child's answers come through a pipe this side owns. cmd.StdoutPipe would close its read
	// end when the child exits, and a child that exits right after answering a panic (code 5) would
	// then lose its answer to "file already closed": a verdict turned into a fault.
	stdout, childOut, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("%w: verifier stdout: %w", verify.ErrVerifierFault, err)
	}
	cmd.Stdout = childOut
	if err := startCommand(cmd); err != nil {
		_ = stdout.Close()
		_ = childOut.Close()
		return nil, fmt.Errorf("%w: start %q: %w", verify.ErrVerifierFault, cfg.Path, err)
	}
	_ = childOut.Close() // the child holds the write end; this side reads until it ends
	p := &process{cmd: cmd, stdin: stdin, stdout: stdout, done: make(chan struct{})}
	go func() {
		_ = cmd.Wait()
		close(p.done)
	}()
	id, code, err := p.roundTrip(nil, cfg.StartTimeout)
	if err != nil || id != ReadyID || code != codeOK {
		p.kill()
		if err == nil {
			err = fmt.Errorf("ready frame has id %d code %d", id, code)
		}
		return nil, fmt.Errorf("%w: verifier did not become ready: %w", verify.ErrVerifierFault, err)
	}
	return p, nil
}

// ErrBinaryPin means the verifier binary is not the one the node is pinned to.
var ErrBinaryPin = fmt.Errorf("%w: the verifier binary does not match its pin", verify.ErrVerifierNotLinked)

// pinnedBinary is the verifier binary, opened once and hashed from that open file. The process is
// started from the same open file where the platform allows it (execPath), so the bytes that were
// hashed are the file that runs: replacing the path or repointing a symlink after the hash starts
// nothing else.
type pinnedBinary struct {
	file *os.File
	path string
}

func (b *pinnedBinary) Close() { _ = b.file.Close() }

// afterPinCheck runs between the pin check and the start of the process. It is nil in production;
// a test sets it to swap the file at the path and prove the swap changes nothing.
var afterPinCheck func()

// openPinned opens cfg.Path once, compares the SHA-256 of what it read from that open file with
// the pin, and returns the open file with the path to start the process from.
func openPinned(cfg Config) (*pinnedBinary, error) {
	// A FIFO or a device at the path must not hang the open (and with it Warm or a block): look
	// first, and open without blocking and without following a link at the file itself. Links in
	// the directories above it are fine (cosmovisor's current is one).
	info, err := os.Lstat(cfg.Path)
	if err != nil {
		return nil, fmt.Errorf("%w: out-of-process verifier binary %q: %w; install the release's orama-orchard-verifier there or point --shielded-verifier at it",
			verify.ErrVerifierNotLinked, cfg.Path, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: %q is not a regular file (mode %s)", ErrBinaryPin, cfg.Path, info.Mode().Type())
	}
	f, err := os.OpenFile(cfg.Path, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("%w: out-of-process verifier binary %q: %w; install the release's orama-orchard-verifier there or point --shielded-verifier at it",
			verify.ErrVerifierNotLinked, cfg.Path, err)
	}
	if err := checkPin(f, cfg); err != nil {
		_ = f.Close()
		return nil, err
	}
	if afterPinCheck != nil {
		afterPinCheck()
	}
	return &pinnedBinary{file: f, path: cfg.Path}, nil
}

// checkPin compares the SHA-256 of the open file with the pin.
func checkPin(f *os.File, cfg Config) error {
	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("%w: stat the verifier binary %q: %w", verify.ErrVerifierNotLinked, cfg.Path, err)
	}
	if !info.Mode().IsRegular() || info.Size() > maxBinaryBytes {
		return fmt.Errorf("%w: %q is not a regular file of at most %d bytes", ErrBinaryPin, cfg.Path, int64(maxBinaryBytes))
	}
	if cfg.SHA256 == "" {
		return fmt.Errorf("%w: no sha256 pin is linked into this oramad for %q (a release build links it: -X chain/app.ShieldedVerifierSHA256)", ErrBinaryPin, cfg.Path)
	}
	h := sha256.New()
	if _, err := io.Copy(h, io.LimitReader(f, maxBinaryBytes+1)); err != nil {
		return fmt.Errorf("%w: read the verifier binary %q: %w", verify.ErrVerifierNotLinked, cfg.Path, err)
	}
	if got := hex.EncodeToString(h.Sum(nil)); !strings.EqualFold(got, cfg.SHA256) {
		return fmt.Errorf("%w: %q has sha256 %s, the pin is %s", ErrBinaryPin, cfg.Path, got, cfg.SHA256)
	}
	return nil
}

func (p *process) exited() bool {
	select {
	case <-p.done:
		return true
	default:
		return false
	}
}

func (p *process) kill() {
	_ = p.cmd.Process.Kill()
	_ = p.stdin.Close()
	<-p.done
	_ = p.stdout.Close()
}

// exchange sends one request and returns the result code. The caller kills the process on error.
func (p *process) exchange(id uint64, sighash, b []byte, timeout time.Duration) (byte, error) {
	frame := make([]byte, 0, 4+idLen+len(sighash)+len(b))
	frame = binary.BigEndian.AppendUint32(frame, uint32(idLen+len(sighash)+len(b)))
	frame = binary.BigEndian.AppendUint64(frame, id)
	frame = append(frame, sighash...)
	frame = append(frame, b...)
	gotID, code, err := p.roundTrip(frame, timeout)
	if err != nil {
		return 0, err
	}
	if gotID != id {
		return 0, fmt.Errorf("answered request %d, want %d", gotID, id)
	}
	return code, nil
}

type reply struct {
	id   uint64
	code byte
	err  error
}

// roundTrip writes frame (nothing when nil) and reads one response frame within timeout. The
// I/O runs in a goroutine so a hung process cannot hold the caller; on timeout the caller kills
// the process, which closes the pipes and ends the goroutine.
func (p *process) roundTrip(frame []byte, timeout time.Duration) (uint64, byte, error) {
	out := make(chan reply, 1)
	go func() {
		if frame != nil {
			if _, err := p.stdin.Write(frame); err != nil {
				out <- reply{err: fmt.Errorf("write request: %w", err)}
				return
			}
		}
		out <- readResponse(p.stdout)
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case r := <-out:
		return r.id, r.code, r.err
	case <-timer.C:
		return 0, 0, fmt.Errorf("no answer within %s", timeout)
	}
}

func readResponse(r io.Reader) reply {
	var head [4]byte
	if _, err := io.ReadFull(r, head[:]); err != nil {
		return reply{err: fmt.Errorf("read response length: %w", err)}
	}
	if n := binary.BigEndian.Uint32(head[:]); n != responseLen {
		return reply{err: errors.New("response frame has the wrong length")}
	}
	var body [responseLen]byte
	if _, err := io.ReadFull(r, body[:]); err != nil {
		return reply{err: fmt.Errorf("read response: %w", err)}
	}
	return reply{id: binary.BigEndian.Uint64(body[:idLen]), code: body[idLen]}
}
