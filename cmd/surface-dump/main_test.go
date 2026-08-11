package main

import (
	"testing"

	"github.com/spf13/cobra"
)

// findSpec returns the commandSpec for a given path, or nil.
func findSpec(specs []commandSpec, path string) *commandSpec {
	for i := range specs {
		if specs[i].Path == path {
			return &specs[i]
		}
	}
	return nil
}

// flagByName returns the flagSpec for a given flag name, or nil.
func flagByName(flags []flagSpec, name string) *flagSpec {
	for i := range flags {
		if flags[i].Name == name {
			return &flags[i]
		}
	}
	return nil
}

// TestWalk builds a toy command tree and asserts the emitted commandSpec slice
// directly, independent of the committed surface.json. The self-generating gate
// (this code diffs its own output) cannot catch a deterministic bug in walk,
// since the manifest it compares against was produced by the same bug — so the
// bug that shipped ("use" left argv-derived on the root) is pinned here instead.
func TestWalk(t *testing.T) {
	// Root Use is deliberately the binary name, not "orq", to prove walk pins it.
	root := &cobra.Command{Use: "surface-dump"}
	// A persistent flag declared ON the root: it lands in both LocalFlags and
	// PersistentFlags, and walk visits local first, so it must be recorded once,
	// marked local (persistent=false).
	root.PersistentFlags().String("output-format", "", "")
	root.PersistentFlags().Bool("no-color", false, "")

	get := &cobra.Command{Use: "get [id]"}
	get.Flags().String("id", "", "")
	get.Flags().StringP("verbose", "v", "", "")

	// A hidden command with an alias, to pin passthrough of both.
	secret := &cobra.Command{Use: "secret", Aliases: []string{"s"}, Hidden: true}

	root.AddCommand(get)
	root.AddCommand(secret)

	var specs []commandSpec
	walk(root, "", &specs)

	// Root: name and Use both pinned to "orq" (the regression: only name was).
	rootSpec := findSpec(specs, "orq")
	if rootSpec == nil {
		t.Fatalf("no root spec at path %q; got paths %v", "orq", paths(specs))
	}
	if rootSpec.Use != "orq" {
		t.Errorf("root Use = %q, want \"orq\" (argv-derived name must be pinned)", rootSpec.Use)
	}

	// The root persistent flag appears exactly once, marked local by the
	// local-first dedup — not duplicated across the local and persistent passes.
	of := flagByName(rootSpec.Flags, "output-format")
	if of == nil {
		t.Fatalf("root missing output-format flag; got %v", rootSpec.Flags)
	}
	if of.Persistent {
		t.Errorf("output-format Persistent = true, want false (local pass runs first)")
	}
	if n := countFlag(rootSpec.Flags, "output-format"); n != 1 {
		t.Errorf("output-format appears %d times, want 1 (dedup)", n)
	}

	// Flags are sorted by name.
	for i := 1; i < len(rootSpec.Flags); i++ {
		if rootSpec.Flags[i-1].Name > rootSpec.Flags[i].Name {
			t.Errorf("root flags not sorted: %q before %q", rootSpec.Flags[i-1].Name, rootSpec.Flags[i].Name)
		}
	}

	// Child: path is composed from the pinned root name, Use passes through, and
	// a shorthand is captured.
	getSpec := findSpec(specs, "orq get")
	if getSpec == nil {
		t.Fatalf("no child spec at path %q; got %v", "orq get", paths(specs))
	}
	if getSpec.Use != "get [id]" {
		t.Errorf("get Use = %q, want \"get [id]\"", getSpec.Use)
	}
	if vf := flagByName(getSpec.Flags, "verbose"); vf == nil || vf.Shorthand != "v" {
		t.Errorf("get verbose flag = %+v, want shorthand \"v\"", vf)
	}

	// Hidden + aliases pass through.
	secretSpec := findSpec(specs, "orq secret")
	if secretSpec == nil {
		t.Fatalf("no hidden spec at path %q", "orq secret")
	}
	if !secretSpec.Hidden {
		t.Errorf("secret Hidden = false, want true")
	}
	if len(secretSpec.Aliases) != 1 || secretSpec.Aliases[0] != "s" {
		t.Errorf("secret Aliases = %v, want [s]", secretSpec.Aliases)
	}
}

func paths(specs []commandSpec) []string {
	out := make([]string, len(specs))
	for i, s := range specs {
		out[i] = s.Path
	}
	return out
}

func countFlag(flags []flagSpec, name string) int {
	n := 0
	for _, f := range flags {
		if f.Name == name {
			n++
		}
	}
	return n
}
