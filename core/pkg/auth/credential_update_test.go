//go:build unix

package auth

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestUpdateEnhancedCredentials_concurrentUpdatesAllSurvive: two commands each
// adding a gateway entry used to load the same file and save over each other,
// so the last writer dropped the other's entry.
func TestUpdateEnhancedCredentials_concurrentUpdatesAllSurvive(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const writers = 16
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			gw := fmt.Sprintf("https://gw%d.example", i)
			err := UpdateEnhancedCredentials(func(s *EnhancedCredentialStore) error {
				s.AddCredential(gw, &Credentials{Wallet: "0xabc", Namespace: "ns"})
				return nil
			})
			if err != nil {
				t.Errorf("update %d: %v", i, err)
			}
		}()
	}
	wg.Wait()

	store, err := LoadEnhancedCredentials()
	if err != nil {
		t.Fatal(err)
	}
	if len(store.Gateways) != writers {
		t.Fatalf("%d gateway entries survived, want %d", len(store.Gateways), writers)
	}
}

func TestUpdateCredentials_concurrentUpdatesAllSurvive(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const writers = 8
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			gw := fmt.Sprintf("https://gw%d.example", i)
			err := UpdateCredentials(func(s *CredentialStore) error {
				s.SetCredentialsForGateway(gw, &Credentials{Wallet: "0xabc"})
				return nil
			})
			if err != nil {
				t.Errorf("update %d: %v", i, err)
			}
		}()
	}
	wg.Wait()
	store, err := LoadCredentials()
	if err != nil {
		t.Fatal(err)
	}
	if len(store.Gateways) != writers {
		t.Fatalf("%d gateway entries survived, want %d", len(store.Gateways), writers)
	}
}

// A failing update saves nothing, and the lock is released on that path: a
// second update completing proves it was not leaked.
func TestUpdateEnhancedCredentials_failedUpdateSavesNothingAndReleasesLock(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	boom := errors.New("boom")
	err := UpdateEnhancedCredentials(func(s *EnhancedCredentialStore) error {
		s.AddCredential("https://gw.example", &Credentials{Wallet: "0xabc"})
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the update's error", err)
	}
	path, _ := GetCredentialsPath()
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("a failed update wrote the credential file (stat err %v)", statErr)
	}

	done := make(chan error, 1)
	go func() { done <- UpdateEnhancedCredentials(func(*EnhancedCredentialStore) error { return nil }) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the lock was still held after a failed update")
	}
}

func TestUpdateEnhancedCredentials_waitsForLockHolder(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	unlock, err := lockCredentialFile()
	if err != nil {
		t.Fatal(err)
	}
	ran := make(chan struct{})
	updated := make(chan error, 1)
	go func() {
		updated <- UpdateEnhancedCredentials(func(*EnhancedCredentialStore) error { close(ran); return nil })
	}()
	// The update saves the file and releases the lock after its callback
	// returns; the test waits for that, so TempDir's cleanup never races it.
	t.Cleanup(func() {
		select {
		case err := <-updated:
			if err != nil {
				t.Errorf("update: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("update did not return")
		}
	})
	select {
	case <-ran:
		t.Fatal("update ran while another holder had the lock")
	case <-time.After(100 * time.Millisecond):
	}
	unlock()
	select {
	case <-ran:
	case <-time.After(5 * time.Second):
		t.Fatal("update did not run after the lock was released")
	}
}

// A lock failure is returned and the update never runs.
func TestUpdateEnhancedCredentials_lockFailureSurfaces(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".orama")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	// A directory where the lock file belongs cannot be opened for writing.
	if err := os.Mkdir(filepath.Join(dir, "credentials.json"+credentialLockSuffix), 0o700); err != nil {
		t.Fatal(err)
	}
	called := false
	err := UpdateEnhancedCredentials(func(*EnhancedCredentialStore) error { called = true; return nil })
	if err == nil || called {
		t.Fatalf("err = %v, update called = %v; want a lock error and no update", err, called)
	}
}

// Loading a legacy file migrates it through the lock exactly once; a nested
// lock would hang this test.
func TestLoadEnhancedCredentials_migrationSavesWithoutNestedLock(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	path, _ := GetCredentialsPath()
	legacy := `{"version":"1.0","gateways":{"https://gw.example":{"wallet":"0xabc","namespace":"ns"}}}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		store, err := LoadEnhancedCredentials()
		if err == nil && len(store.Gateways["https://gw.example"].Credentials) != 1 {
			err = errors.New("migrated store lost the credential")
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("migration deadlocked on the credential lock")
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), `"credentials"`) {
		t.Fatalf("migration was not saved in the enhanced format: %s", raw)
	}
}

func TestSelectCredential_findsByIdentityAndMissing(t *testing.T) {
	s := &EnhancedCredentialStore{Gateways: map[string]*GatewayCredentials{}}
	s.AddCredential("g", &Credentials{Wallet: "0xAAA", Namespace: "a"})
	s.AddCredential("g", &Credentials{Wallet: "0xBBB", Namespace: "b"})
	if s.SelectCredential("g", "0xbbb", "b") == nil || s.Gateways["g"].DefaultIndex != 1 {
		t.Fatal("did not select the credential by wallet and namespace")
	}
	if s.SelectCredential("g", "0xbbb", "other") != nil || s.SelectCredential("nope", "0xbbb", "b") != nil {
		t.Fatal("selected a credential that is not stored")
	}
}
