//go:build cgo && orchardffi

package orchardproc

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sort"
	"syscall"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/Orama-Network/orama/chain/x/shielded/bundle"
	"github.com/Orama-Network/orama/chain/x/shielded/verify"
)

// The differential harness (SA3). The two verifiers of x/shielded are the linked library and the
// out-of-process binary. They are two builds over one implementation, with two bundle readers (the
// library's own, zcash_primitives' in the binary), so they must agree on every input: the same
// accept or reject, and the same class of reason. A bundle that one accepts and the
// other refuses would split the verifiers; a node that runs both refuses it, but a verdict that
// depends on which build looked is the signature of a build, ABI or reader bug, and this harness is
// what finds it. The valid bundles are the committed vectors the wallet crate builds with real proofs
// (chain/x/shielded/orchardffi/testdata); everything else is derived from them.

// The classes a verifier's answer falls in. A fault is not a verdict: no input may produce one.
const (
	classAccept    = "accept"
	classMalformed = "malformed"
	classProofLen  = "proof-length"
	classProof     = "proof-rejected"
	classSignature = "signature-rejected"
	classPanic     = "panic"
	classFault     = "fault"
)

func classOf(err error) string {
	switch {
	case err == nil:
		return classAccept
	case errors.Is(err, verify.ErrMalformed):
		return classMalformed
	case errors.Is(err, verify.ErrProofLength):
		return classProofLen
	case errors.Is(err, verify.ErrProofRejected):
		return classProof
	case errors.Is(err, verify.ErrSignatureRejected):
		return classSignature
	case errors.Is(err, verify.ErrVerifierPanic):
		return classPanic
	case errors.Is(err, verify.ErrVerifierFault):
		return classFault
	default:
		return "other: " + err.Error()
	}
}

// pair is both verifiers over the same chain id.
type pair struct {
	library verify.Verifier
	process *Verifier
}

func newPair(t *testing.T, chainID string, timeout time.Duration) pair {
	t.Helper()
	v := mustNew(t, Config{Path: binaryPath(t), ChainID: chainID, RequestTimeout: timeout})
	t.Cleanup(func() { _ = v.Close() })
	return pair{library: mustLibrary(t, chainID), process: v}
}

// agree verifies in with both and fails the test unless they give the same class. It returns it.
func (p pair) agree(t testing.TB, label string, in, binding []byte) string {
	t.Helper()
	lib, proc := classOf(p.library.Verify(in, binding)), classOf(p.process.Verify(in, binding))
	if lib != proc {
		t.Errorf("%s: the library says %q, the process says %q (%d bytes)", label, lib, proc, len(in))
	}
	if lib == classFault || proc == classFault {
		t.Errorf("%s: a verifier faulted on a bundle; no input may do that (library %q, process %q)", label, lib, proc)
	}
	return lib
}

// fieldOffsets are positions in a bundle with n actions, one in every field.
func fieldOffsets(raw []byte) map[string]int {
	n := int(raw[0])
	proofAt := 1 + n*bundle.ActionLen + bundle.HeaderLen
	sigsAt := proofAt + bundle.ProofLen(n)
	return map[string]int{
		"action count":    0,
		"cv":              1 + 3,
		"nullifier":       1 + 32 + 3,
		"rk":              1 + 64 + 3,
		"cmx":             1 + 96 + 3,
		"epk":             1 + 128 + 3,
		"ciphertext":      1 + 160 + 100,
		"out ciphertext":  1 + 160 + 580 + 7,
		"flags":           1 + n*bundle.ActionLen,
		"value balance":   1 + n*bundle.ActionLen + 3,
		"anchor":          1 + n*bundle.ActionLen + 9 + 4,
		"proof start":     proofAt,
		"proof middle":    proofAt + bundle.ProofLen(n)/2,
		"proof end":       sigsAt - 1,
		"spend auth sig":  sigsAt + 5,
		"binding sig":     len(raw) - 10,
		"binding sig end": len(raw) - 1,
	}
}

type mutation struct {
	label string
	bytes []byte
}

// mutations are the adversarial variants of one valid bundle.
func mutations(raw []byte) []mutation {
	var out []mutation
	add := func(label string, b []byte) { out = append(out, mutation{label, b}) }
	offsets := fieldOffsets(raw)
	names := make([]string, 0, len(offsets))
	for name := range offsets {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		at := offsets[name]
		for bit := 0; bit < 8; bit += 7 { // the lowest and the highest bit
			m := bytes.Clone(raw)
			m[at] ^= 1 << bit
			add(fmt.Sprintf("%s: flip bit %d", name, bit), m)
		}
		for _, fill := range []byte{0x00, 0xff} {
			if raw[at] == fill {
				continue // not a change
			}
			m := bytes.Clone(raw)
			m[at] = fill
			add(fmt.Sprintf("%s: byte set to %#x", name, fill), m)
		}
	}
	n := int(raw[0])
	for _, count := range []byte{0, byte(n + 1), byte(n - 1), 100, 252} {
		if int(count) != n {
			m := bytes.Clone(raw)
			m[0] = count
			add(fmt.Sprintf("action count byte %d, the bundle holds %d", count, n), m)
		}
	}
	add("action count as a non-canonical compact size", append([]byte{0xfd, byte(n), 0}, raw[1:]...))
	add("action count as a four byte compact size", append([]byte{0xfe, byte(n), 0, 0, 0}, raw[1:]...))
	add("action count that overflows a size", append([]byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x7f}, raw[1:]...))
	proofAt := 1 + n*bundle.ActionLen + bundle.HeaderLen
	for _, cut := range []int{0, 1, 2, bundle.ActionLen, proofAt - 1, proofAt, proofAt + 1, len(raw) - bundle.SigLen - 1, len(raw) - 1} {
		if cut < len(raw) {
			add(fmt.Sprintf("truncated to %d of %d bytes", cut, len(raw)), bytes.Clone(raw[:cut]))
		}
	}
	for _, extra := range []int{1, 64, 4096} {
		add(fmt.Sprintf("%d trailing bytes", extra), append(bytes.Clone(raw), make([]byte, extra)...))
	}
	add("a proof one byte short, the signatures shifted", append(bytes.Clone(raw[:proofAt]), raw[proofAt+1:]...))
	add("the bundle twice", append(bytes.Clone(raw), raw...))
	add("all zero bytes of the same length", make([]byte, len(raw)))
	add("one byte", []byte{raw[0]})
	add("oversize by one byte", make([]byte, bundle.MaxBytes+1))
	add("exactly the size limit, zeros", make([]byte, bundle.MaxBytes))
	return out
}

func TestDifferential_bothVerifiersAgreeOnTheCorpus(t *testing.T) {
	_, chainID := load(t, "ironwood-1-action")
	p := newPair(t, chainID, 30*time.Second)
	otherBinding := bytes.Repeat([]byte{7}, 32)
	cases := 0
	for _, name := range vectors {
		raw, _ := load(t, name)
		untouched := p.agree(t, name+"/untouched", raw, nil)
		cases++
		if name == vectors[0] || name == vectors[1] || name == vectors[2] {
			if untouched != classAccept {
				t.Errorf("%s: both verifiers must accept the committed vector, got %q", name, untouched)
			}
		}
		// A binding the signatures do not commit to turns every valid bundle into a refusal.
		if got := p.agree(t, name+"/foreign binding", raw, otherBinding); got != classSignature {
			t.Errorf("%s with a binding it was not signed for: %q, want %q", name, got, classSignature)
		}
		cases++
		for _, m := range mutations(raw) {
			got := p.agree(t, name+"/"+m.label, m.bytes, nil)
			if got == classAccept {
				t.Errorf("%s/%s: a tampered bundle was accepted by both", name, m.label)
			}
			cases++
		}
	}
	t.Logf("%d inputs: the library and the process gave the same class for every one", cases)
}

// A bundle signed for one chain is refused on another by both, in the signature check.
func TestDifferential_aForeignChainIsRefusedByBoth(t *testing.T) {
	_, chainID := load(t, "ironwood-1-action")
	p := newPair(t, chainID+"-other", 30*time.Second)
	for _, name := range vectors[:3] {
		raw, _ := load(t, name)
		if got := p.agree(t, name, raw, nil); got != classSignature {
			t.Errorf("%s on a foreign chain: %q, want %q", name, got, classSignature)
		}
	}
}

// Property: for any few bit flips, truncation or extension of any committed vector, the two
// verifiers agree on accept or reject and on the class, and a bundle that differs from a vector is
// never accepted. This is the "no bundle is accepted by one and refused by the other" claim,
// searched rather than listed.
func TestDifferential_propertyNoBundleSplitsTheVerifiers(t *testing.T) {
	_, chainID := load(t, "ironwood-1-action")
	p := newPair(t, chainID, 30*time.Second)
	raws := make([][]byte, len(vectors))
	for i, name := range vectors {
		raws[i], _ = load(t, name)
	}
	rapid.Check(t, func(rt *rapid.T) {
		raw := raws[rapid.IntRange(0, len(raws)-1).Draw(rt, "vector")]
		in := bytes.Clone(raw)
		for _, flip := range rapid.SliceOfN(rapid.Custom(func(rt *rapid.T) [2]int {
			return [2]int{rapid.IntRange(0, len(raw)-1).Draw(rt, "at"), rapid.IntRange(0, 7).Draw(rt, "bit")}
		}), 0, 4).Draw(rt, "flips") {
			in[flip[0]] ^= 1 << flip[1]
		}
		switch rapid.IntRange(0, 3).Draw(rt, "shape") {
		case 1:
			in = in[:rapid.IntRange(0, len(in)).Draw(rt, "cut")]
		case 2:
			in = append(in, rapid.SliceOfN(rapid.Byte(), 1, 300).Draw(rt, "tail")...)
		}
		binding := rapid.SampledFrom([][]byte{nil, bytes.Repeat([]byte{1}, 32)}).Draw(rt, "binding")
		lib, proc := classOf(p.library.Verify(in, binding)), classOf(p.process.Verify(in, binding))
		if lib != proc {
			rt.Fatalf("the library says %q and the process says %q", lib, proc)
		}
		if lib == classFault || lib == classPanic {
			rt.Fatalf("a bundle made a verifier fault or panic: %q", lib)
		}
		if lib == classAccept && (binding != nil || !bytes.Equal(in, raw) || !isUnbound(raw, raws)) {
			rt.Fatalf("a changed bundle or a foreign binding was accepted by both")
		}
	})
}

// isUnbound reports whether raw is one of the vectors that verify with no binding.
func isUnbound(raw []byte, raws [][]byte) bool {
	for _, r := range raws[:3] {
		if bytes.Equal(r, raw) {
			return true
		}
	}
	return false
}

// Timing never decides a verdict. A child that is stopped for a while in the middle of a request
// and then continues gives the library's verdict. A child that is stopped past the watchdog gives a
// fault, which is not a verdict: the caller halts on it in a block and never records it.
func TestDifferential_timingOutliersChangeNoVerdict(t *testing.T) {
	_, chainID := load(t, "ironwood-1-action")
	p := newPair(t, chainID, 30*time.Second)
	raw, _ := load(t, "ironwood-2-action")
	bad := bytes.Clone(raw)
	bad[len(bad)-10] ^= 1
	for label, in := range map[string][]byte{"valid": raw, "tampered": bad} {
		want := classOf(p.library.Verify(in, nil))
		if err := p.process.Warm(); err != nil {
			t.Fatal(err)
		}
		child := p.process.proc.cmd.Process
		if err := child.Signal(syscall.SIGSTOP); err != nil {
			t.Fatal(err)
		}
		go func() {
			time.Sleep(500 * time.Millisecond)
			_ = child.Signal(syscall.SIGCONT)
		}()
		start := time.Now()
		got := classOf(p.process.Verify(in, nil))
		if got != want {
			t.Errorf("%s: a child stopped for 500 ms gave %q, the library %q", label, got, want)
		}
		if took := time.Since(start); took < 400*time.Millisecond {
			t.Errorf("%s: the stop did not delay the request (%s)", label, took)
		}
	}

	slow := newPair(t, chainID, 300*time.Millisecond)
	if err := slow.process.Warm(); err != nil {
		t.Fatal(err)
	}
	child := slow.process.proc.cmd.Process
	if err := child.Signal(syscall.SIGSTOP); err != nil {
		t.Fatal(err)
	}
	if got := classOf(slow.process.Verify(raw, nil)); got != classFault {
		t.Errorf("a child stopped past the watchdog gave %q, want %q: a timeout is not a verdict", got, classFault)
	}
	if got := classOf(slow.process.Verify(raw, nil)); got != classAccept {
		t.Errorf("the next request, on a fresh process, gave %q, want %q", got, classAccept)
	}
}

// The latency of the process verifier on every vector. It asserts that no request faults, which is
// what the watchdog (30 s) turns a hang into, and that none came close to it on a valid bundle.
func TestDifferential_latencyIsFarBelowTheWatchdog(t *testing.T) {
	_, chainID := load(t, "ironwood-1-action")
	p := newPair(t, chainID, 30*time.Second)
	if err := p.process.Warm(); err != nil { // the first request would otherwise include the key build
		t.Fatal(err)
	}
	for _, name := range vectors {
		raw, _ := load(t, name)
		var took []time.Duration
		for i := 0; i < 7; i++ {
			start := time.Now()
			if got := classOf(p.process.Verify(raw, nil)); got == classFault {
				t.Fatalf("%s: request %d faulted (the watchdog fired or the child died)", name, i)
			}
			took = append(took, time.Since(start))
		}
		sort.Slice(took, func(i, j int) bool { return took[i] < took[j] })
		median, slowest := took[len(took)/2], took[len(took)-1]
		t.Logf("%s: median %s, slowest %s", name, median, slowest)
		// A real hang ends in a fault at the watchdog and fails above. This bound is only a slow
		// box that did not hang: it is not a multiple of a median a loaded machine inflates.
		if slowest >= DefaultRequestTimeout {
			t.Errorf("%s: the slowest of 7 requests took %s, the watchdog is %s", name, slowest, DefaultRequestTimeout)
		}
	}
}

// rawChild is the verifier binary spoken to by hand, for frames Verify would never send.
type rawChild struct {
	cmd *exec.Cmd
	in  io.WriteCloser
	out io.Reader
}

func startRawChild(t *testing.T) *rawChild {
	t.Helper()
	cmd := exec.Command(binaryPath(t))
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	c := &rawChild{cmd: cmd, in: in, out: out}
	if r := readResponse(out); r.err != nil || r.id != ReadyID || r.code != codeOK {
		t.Fatalf("no ready frame: %+v", r)
	}
	return c
}

func (c *rawChild) send(t *testing.T, length uint32, body []byte) {
	t.Helper()
	frame := binary.BigEndian.AppendUint32(nil, length)
	if _, err := c.in.Write(append(frame, body...)); err != nil {
		t.Fatal(err)
	}
}

// The child's own limits, behind the ones Verify applies. An oversize frame ends the child without
// an answer (so it can only ever reach the node as a fault, which is why Verify refuses an oversize
// bundle itself, as a verdict, before it sends anything); a request too short to carry a bundle is
// answered malformed; both match what the library says of the same bytes.
func TestDifferential_theChildsFrameLimits(t *testing.T) {
	_, chainID := load(t, "ironwood-1-action")
	p := newPair(t, chainID, 30*time.Second)
	// The child's own limit is its constant MAX_FRAME (orchardverifier-bin protocol.rs), a 1 MiB
	// bundle plus the id and the sighash; the node refuses a bundle over bundle.MaxBytes long before.
	const childMaxFrame = (1 << 20) + idLen + 32
	maxFrame := uint32(childMaxFrame)

	c := startRawChild(t)
	c.send(t, maxFrame+1, nil)
	if r := readResponse(c.out); r.err == nil {
		t.Fatalf("a frame over the limit was answered: %+v", r)
	}
	if err := c.cmd.Wait(); err == nil {
		t.Fatal("the child must exit with an error on an oversize frame")
	}

	c = startRawChild(t)
	short := append(binary.BigEndian.AppendUint64(nil, 42), make([]byte, 32)...)
	c.send(t, uint32(len(short)), short)
	if r := readResponse(c.out); r.err != nil || r.id != 42 || r.code != codeMalformed {
		t.Fatalf("a request with no bundle: %+v, want malformed", r)
	}

	if got := classOf(p.process.Verify(make([]byte, bundle.MaxBytes+1), nil)); got != classMalformed {
		t.Errorf("an oversize bundle through Verify: %q, want %q", got, classMalformed)
	}
	if lib := classOf(p.library.Verify(make([]byte, bundle.MaxBytes+1), nil)); lib != classMalformed {
		t.Errorf("an oversize bundle through the library: %q, want %q", lib, classMalformed)
	}
}
