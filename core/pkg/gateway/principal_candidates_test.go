package gateway

import "testing"

// A key's principal is recorded under the key's stored form, and the caller
// passes that form: the only candidate is the value as given, never a hash of
// it. Hashing it again is what refused every exchanged key token on an owned
// route (change-975).

func TestPrincipalCandidates_apiKeyIsTheStoredFormAsGiven(t *testing.T) {
	got := principalIdentifierCandidates("api_key", "STORED")
	if len(got) != 1 || got[0] != "STORED" {
		t.Errorf("api_key: want [STORED] only; got %v", got)
	}
}

// A wallet stored before normalisation kept whatever case the client sent.
// Ownership lookups must therefore try the normalised spelling first and the
// presented one second, so those rows keep working until migration 042 has run.
func TestOwnerCandidates_WalletChecksTheNormalisedFormFirst(t *testing.T) {
	got := principalIdentifierCandidates("wallet", "0xAbCdEf")
	want := []string{"0xabcdef", "0xAbCdEf"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

// An already-lowercase wallet needs only one candidate.
func TestOwnerCandidates_LowercaseWalletIsASingleCandidate(t *testing.T) {
	got := principalIdentifierCandidates("wallet", "0xabcdef")
	if len(got) != 1 || got[0] != "0xabcdef" {
		t.Fatalf("got %v, want a single normalised candidate", got)
	}
}
