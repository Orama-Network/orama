// Package verify is the hook the Orchard/Ironwood verifiers fill.
//
// The first verifier is the orchard library (upstream orchard 0.15.x over cgo); the second is the
// same upstream crate built into a separate binary and run as a child process
// (x/shielded/verify/orchardproc). Two verifiers must accept a bundle, so with one linked, Check
// still fails closed, and a missing verifier fails closed.
//
// The two are two builds and two processes over ONE implementation, not two implementations: both
// check with the upstream orchard crate, and only the code that reads the bundle's bytes differs
// (the library's own reader, zcash_primitives' in the binary). They protect against a
// memory-corruption bug, a link or ABI problem, a hang or a bad dependency bump in one build, and a
// bug in one reader. They do not protect against a logic bug in the upstream orchard crate or in
// the wrapper both builds carry (framing, the sighash input, the key version, the pool): a bundle
// that exploits one is accepted by both. This package does not implement a circuit.
package verify

import (
	"errors"
	"fmt"
	"time"
)

// MinVerifiers is the number of distinct verifier builds that must accept a bundle. They are
// distinct processes and builds, not independent implementations (see the package comment).
const MinVerifiers = 2

// ErrVerifierNotLinked means no proof checker is registered.
var ErrVerifierNotLinked = errors.New("shielded proof verifier is not linked")

// ErrTampered is returned by tests and by a verifier that rejects bytes.
var ErrTampered = errors.New("shielded bundle rejected")

// The verifier rejection reasons. Each one wraps ErrTampered.
var (
	// ErrMalformed means the bytes are not a canonical Ironwood bundle.
	ErrMalformed = fmt.Errorf("%w: malformed encoding", ErrTampered)
	// ErrProofLength means the proof is not the canonical size for the action count.
	ErrProofLength = fmt.Errorf("%w: proof length is not canonical for the action count", ErrTampered)
	// ErrProofRejected means the Halo 2 proof does not verify.
	ErrProofRejected = fmt.Errorf("%w: proof rejected", ErrTampered)
	// ErrSignatureRejected means a spend-authorization or the binding signature does not
	// verify over the bundle's sighash.
	ErrSignatureRejected = fmt.Errorf("%w: signature rejected", ErrTampered)
)

// ErrDuplicateVerifier means two of the supplied verifiers share an identity, or one has none, so
// the same build would count twice. It wraps ErrVerifierNotLinked: the required number of distinct
// verifiers is not linked.
var ErrDuplicateVerifier = fmt.Errorf("%w: verifiers must be distinct and named", ErrVerifierNotLinked)

// ErrEmptyChainID means a verifier was asked to bind to no chain. A bundle signed for the empty
// chain id would verify on every chain that forgot to set one.
var ErrEmptyChainID = errors.New("shielded verifier needs a chain id")

// ErrVerifierPanic means a verifier caught a panic while checking this bundle. The same bytes
// panic the same code on every node, so this is a verdict on the bundle like any other rejection,
// and it wraps ErrTampered: every node refuses the bundle alike.
var ErrVerifierPanic = fmt.Errorf("%w: the verifier panicked on this bundle", ErrTampered)

// ErrVerifierFault means the verifier could not give an answer: its process died, hung, answered
// out of protocol or would not start, or a call into it was malformed. It is a fact about this
// node's machine, not about the bundle, so it must never become a transaction result: in a block
// the node stops (x/shielded/keeper VerifierHalt) and re-executes the block after a restart, and
// only the mempool refuses the transaction, locally.
var ErrVerifierFault = errors.New("shielded verifier fault")

// DeadlineVerifier is a Verifier that can be given a time limit for one request (the whole request,
// including any wait for the verifier). A caller with a deadline of its own uses it when it can.
type DeadlineVerifier interface {
	Verifier
	VerifyWithin(bundle, binding []byte, d time.Duration) error
}

// ErrVerifierStarting means a verifier of the mempool is not ready yet (its process is being
// started in the background). It is neither a verdict on the bundle nor a fault: nothing was asked
// of the verifier, so nothing is remembered, and the caller may try again in a moment.
var ErrVerifierStarting = errors.New("shielded verifier is starting")

// Verifier checks one bundle. Nodes only verify. They do not build proofs.
//
// binding is what the signatures must additionally commit to: nil for a bundle whose transparent
// side needs no protection, and for an unshield the signer and target, so a copied bundle cannot be
// redirected to another account (see orchard.Sighash).
type Verifier interface {
	// ID names the build (for example "orchard"). Two verifiers count as distinct only when their
	// IDs differ, so the same verifier passed twice cannot satisfy MinVerifiers.
	ID() string
	Verify(bundle, binding []byte) error
}

// Check runs every linked verifier. All of them must accept. Fewer than MinVerifiers, a missing
// verifier, fewer than MinVerifiers distinct verifier IDs, or a single rejection, fails the bundle.
// No verifier runs unless the whole set is valid.
func Check(bundle, binding []byte, verifiers ...Verifier) error {
	if len(verifiers) < MinVerifiers {
		return ErrVerifierNotLinked
	}
	ids := make(map[string]struct{}, len(verifiers))
	for _, v := range verifiers {
		if v == nil {
			return ErrVerifierNotLinked
		}
		id := v.ID()
		if _, dup := ids[id]; dup || id == "" {
			return ErrDuplicateVerifier
		}
		ids[id] = struct{}{}
	}
	for _, v := range verifiers {
		if err := v.Verify(bundle, binding); err != nil {
			return err
		}
	}
	return nil
}
