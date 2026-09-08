//go:build unix

package custom

import (
	"os"
	"testing"
)

// withStdin points os.Stdin at f for the duration of the test.
func withStdin(t *testing.T, f *os.File) {
	t.Helper()
	previous := os.Stdin
	os.Stdin = f
	t.Cleanup(func() { os.Stdin = previous })
}

// An open stdin nobody writes to is what a CI runner, a task runner and
// subprocess.Popen hand a child by default, and `some-cmd | orq traces search`
// looks the same from here. None of them supplied a body, so the default
// window still has to be filled in — a char-device test reads every one of
// them as a body and hands the user back the "from is required" failure.
func TestTracesSearchDefaultsUnderAnIdleStdinPipe(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reader.Close(); writer.Close() })
	withStdin(t, reader)

	root, got := fakeTracesSearch(t)
	root.SetArgs([]string{"traces", "search"})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if from, to := got(); from != "7d" || to != "now" {
		t.Errorf("window = %q..%q, want 7d..now", from, to)
	}
}

// A body actually waiting on the pipe is sent as given, so neither end may be
// rewritten from under it.
func TestTracesSearchLeavesAPipedBodyAlone(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reader.Close() })
	if _, err := writer.WriteString(`{"from":"90d"}`); err != nil {
		t.Fatal(err)
	}
	writer.Close()
	withStdin(t, reader)

	root, got := fakeTracesSearch(t)
	root.SetArgs([]string{"traces", "search"})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if from, to := got(); from != "" || to != "" {
		t.Errorf("window = %q..%q, want both empty", from, to)
	}
}

// `orq traces search < body.json` is the redirect form of the same thing.
func TestTracesSearchLeavesARedirectedBodyAlone(t *testing.T) {
	path := t.TempDir() + "/body.json"
	if err := os.WriteFile(path, []byte(`{"from":"90d"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { file.Close() })
	withStdin(t, file)

	root, got := fakeTracesSearch(t)
	root.SetArgs([]string{"traces", "search"})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if from, to := got(); from != "" || to != "" {
		t.Errorf("window = %q..%q, want both empty", from, to)
	}
}
