package configpaths_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/yiivgeny/wireproxy-pool/internal/configpaths"
)

func TestExpandPreservesArgumentOrderSortsGlobAndDeduplicates(t *testing.T) {
	directory := t.TempDir()
	a := writeConfig(t, directory, "a.conf")
	b := writeConfig(t, directory, "b.conf")
	_ = writeConfig(t, directory, "ignored.txt")

	got, err := configpaths.Expand([]string{b, filepath.Join(directory, "*.conf"), a})
	if err != nil {
		t.Fatalf("Expand() error = %v", err)
	}
	want := []string{b, a}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Expand() = %v, want %v", got, want)
	}
}

func TestExpandReportsInvalidAndUnmatchedPatterns(t *testing.T) {
	tests := []struct {
		name   string
		inputs []string
		want   string
	}{
		{name: "none", want: "no config paths"},
		{name: "invalid", inputs: []string{"["}, want: "invalid config pattern"},
		{name: "unmatched", inputs: []string{filepath.Join(t.TempDir(), "*.conf")}, want: "matched no files"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := configpaths.Expand(test.inputs)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Expand() error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestBackendIDIsStableAndPathSpecific(t *testing.T) {
	first := configpaths.BackendID("/one/primary.conf")
	if first != configpaths.BackendID("/one/primary.conf") {
		t.Fatal("BackendID() is not stable")
	}
	if first == configpaths.BackendID("/two/primary.conf") {
		t.Fatal("BackendID() does not disambiguate equal basenames")
	}
	if !strings.HasPrefix(first, "primary-") {
		t.Fatalf("BackendID() = %q, want readable prefix", first)
	}
}

func writeConfig(t *testing.T, directory, name string) string {
	t.Helper()
	path := filepath.Join(directory, name)
	if err := os.WriteFile(path, []byte("test"), 0o600); err != nil {
		t.Fatal(err)
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		t.Fatal(err)
	}
	return canonical
}
