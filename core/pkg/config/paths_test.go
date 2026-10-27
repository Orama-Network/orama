package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExpandPath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("failed to get home directory: %v", err)
	}

	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{
			name:  "tilde expands to home directory",
			input: "~",
			want:  home,
		},
		{
			name:  "tilde with subdir expands correctly",
			input: "~/subdir",
			want:  filepath.Join(home, "subdir"),
		},
		{
			name:  "tilde with nested subdir expands correctly",
			input: "~/a/b/c",
			want:  filepath.Join(home, "a", "b", "c"),
		},
		{
			name:  "absolute path stays unchanged",
			input: "/usr/local/bin",
			want:  "/usr/local/bin",
		},
		{
			name:  "relative path stays unchanged",
			input: "relative/path",
			want:  "relative/path",
		},
		{
			name:  "dot path stays unchanged",
			input: "./local",
			want:  "./local",
		},
		{
			name:  "empty path returns empty",
			input: "",
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ExpandPath(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("ExpandPath(%q) returned unexpected error: %v", tt.input, err)
			}
			if got != tt.want {
				t.Errorf("ExpandPath(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestExpandPathWithEnvVar(t *testing.T) {
	t.Run("expands environment variable", func(t *testing.T) {
		t.Setenv("TEST_EXPAND_DIR", "/custom/path")

		got, err := ExpandPath("$TEST_EXPAND_DIR/subdir")
		if err != nil {
			t.Fatalf("ExpandPath() returned error: %v", err)
		}
		if got != "/custom/path/subdir" {
			t.Errorf("expected %q, got %q", "/custom/path/subdir", got)
		}
	})

	t.Run("unset env var expands to empty", func(t *testing.T) {
		// Ensure the var is not set
		t.Setenv("TEST_UNSET_VAR_XYZ", "")
		os.Unsetenv("TEST_UNSET_VAR_XYZ")

		got, err := ExpandPath("$TEST_UNSET_VAR_XYZ/subdir")
		if err != nil {
			t.Fatalf("ExpandPath() returned error: %v", err)
		}
		// os.ExpandEnv replaces unset vars with ""
		if got != "/subdir" {
			t.Errorf("expected %q, got %q", "/subdir", got)
		}
	})
}

func TestExpandPathTildeResult(t *testing.T) {
	t.Run("tilde result does not contain tilde", func(t *testing.T) {
		got, err := ExpandPath("~/something")
		if err != nil {
			t.Fatalf("ExpandPath() returned error: %v", err)
		}
		if strings.Contains(got, "~") {
			t.Errorf("expanded path should not contain ~, got %q", got)
		}
	})

	t.Run("tilde result is absolute", func(t *testing.T) {
		got, err := ExpandPath("~/something")
		if err != nil {
			t.Fatalf("ExpandPath() returned error: %v", err)
		}
		if !filepath.IsAbs(got) {
			t.Errorf("expanded tilde path should be absolute, got %q", got)
		}
	})
}

func TestConfigDir(t *testing.T) {
	t.Run("returns path ending with .orama", func(t *testing.T) {
		dir, err := ConfigDir()
		if err != nil {
			t.Fatalf("ConfigDir() returned error: %v", err)
		}
		if !strings.HasSuffix(dir, ".orama") {
			t.Errorf("expected path ending with .orama, got %q", dir)
		}
	})

	t.Run("returns absolute path", func(t *testing.T) {
		dir, err := ConfigDir()
		if err != nil {
			t.Fatalf("ConfigDir() returned error: %v", err)
		}
		if !filepath.IsAbs(dir) {
			t.Errorf("expected absolute path, got %q", dir)
		}
	})

	t.Run("path is under home directory", func(t *testing.T) {
		home, err := os.UserHomeDir()
		if err != nil {
			t.Fatalf("failed to get home dir: %v", err)
		}
		dir, err := ConfigDir()
		if err != nil {
			t.Fatalf("ConfigDir() returned error: %v", err)
		}
		expected := filepath.Join(home, ".orama")
		if dir != expected {
			t.Errorf("expected %q, got %q", expected, dir)
		}
	})
}

func TestDefaultPath(t *testing.T) {
	t.Run("absolute path returned as-is", func(t *testing.T) {
		absPath := "/absolute/path/to/config.yaml"
		got, err := DefaultPath(absPath)
		if err != nil {
			t.Fatalf("DefaultPath() returned error: %v", err)
		}
		if got != absPath {
			t.Errorf("expected %q, got %q", absPath, got)
		}
	})

	t.Run("relative component returns path under orama dir", func(t *testing.T) {
		// DefaultPath looks for existing files under ~/.orama; the test's
		// home is its own, not whatever the machine running it holds.
		t.Setenv("HOME", t.TempDir())
		got, err := DefaultPath("node.yaml")
		if err != nil {
			t.Fatalf("DefaultPath() returned error: %v", err)
		}
		if !filepath.IsAbs(got) {
			t.Errorf("expected absolute path, got %q", got)
		}
		if !strings.Contains(got, ".orama") {
			t.Errorf("expected path containing .orama, got %q", got)
		}
	})
}
