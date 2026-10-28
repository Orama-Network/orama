package serverless

import (
	"context"
	"testing"

	"github.com/Orama-Network/orama/core/pkg/gateway/auth"
	"go.uber.org/zap"
)

// denyRecorder records the deny_device_with_evidence calls that reached the
// gateway's services.
type denyRecorder struct {
	*MockHostServices
	denied int
}

func (d *denyRecorder) DenyDeviceWithEvidence(context.Context, string, string, string) error {
	d.denied++
	return nil
}

// feat-976 review: deny_device_with_evidence copied the evidence out of guest
// memory before the 16 KiB bound and the invocation's call budget refused the
// call. Both refuse it now before anything is read.
func TestDenyDeviceWithEvidence_refusesBeforeReadingGuestMemory(t *testing.T) {
	svc := &denyRecorder{MockHostServices: NewMockHostServices()}
	e, err := NewEngine(nil, NewMockRegistry(), svc, zap.NewNop())
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	t.Cleanup(func() { _ = e.runtime.Close(context.Background()) })
	mod, err := e.runtime.Instantiate(context.Background(), guestMemory)
	if err != nil {
		t.Fatal(err)
	}
	const subject, device, evidenceAt = "0xwallet", "dev-1", 64
	mod.Memory().Write(0, []byte(subject))
	mod.Memory().Write(16, []byte(device))
	call := func(ctx context.Context, evLen uint32) uint32 {
		return e.hDenyDeviceWithEvidence(ctx, mod, 0, uint32(len(subject)), 16, uint32(len(device)), evidenceAt, evLen)
	}

	// Past the bound, though inside guest memory: refused, nothing denied.
	if got := call(WithDeviceAdmissionCounter(context.Background()), auth.MaxRevocationEvidenceBytes+1); got != 0 || svc.denied != 0 {
		t.Errorf("evidence past the bound: returned %d, %d denials reached the gateway", got, svc.denied)
	}

	// The budget spent: refused, nothing denied.
	spent := WithDeviceAdmissionCounter(context.Background())
	for i := 0; i < MaxDeviceAdmissionsPerInvocation; i++ {
		AddDeviceAdmissionCount(spent)
	}
	if got := call(spent, 8); got != 0 || svc.denied != 0 {
		t.Errorf("past the budget: returned %d, %d denials reached the gateway", got, svc.denied)
	}

	// Within both: the call goes through.
	if got := call(WithDeviceAdmissionCounter(context.Background()), auth.MaxRevocationEvidenceBytes); got != 1 || svc.denied != 1 {
		t.Errorf("evidence at the bound: returned %d, %d denials, want 1 and 1", got, svc.denied)
	}
}
