package skills

import (
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Q3: a permanent record whose symlink DANGLES into a collected generation.
// Does anything repair it?
func TestScratchDanglingPermanentLink(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if _, err := Install([]string{"claude"}); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, ".claude", "skills")
	names, _ := Names()
	victim := filepath.Join(dir, names[0])

	// Make it dangle: repoint at a generation that no longer exists, still
	// inside the snapshot tree (which is what collection leaves behind).
	orqHome, _ := Home()
	dead := filepath.Join(orqHome, "snapshot", "gen-collected", names[0])
	if err := os.Remove(victim); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(dead, victim); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(victim); err == nil {
		t.Fatal("expected a dangling link")
	}
	if !exists(victim) {
		t.Fatal("exists() should report a dangling symlink as existing (Lstat)")
	}

	// isOurs on a dangling link into the snapshot?
	m, _ := LoadManifest()
	l := findLink(m, victim)
	t.Logf("isOurs(dangling-into-snapshot) = %v", isOurs(*l))

	// (a) refresh with the SAME fingerprint (the common case).
	if _, err := Refresh(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(victim); err == nil {
		t.Log("REFRESH(same fp): repaired")
	} else {
		t.Log("REFRESH(same fp): STILL DANGLING")
	}

	// (b) InstallSession.
	rel, err := InstallSession("claude")
	if err != nil {
		t.Fatal(err)
	}
	rel()
	if _, err := os.Stat(victim); err == nil {
		t.Log("INSTALLSESSION: repaired")
	} else {
		t.Log("INSTALLSESSION: STILL DANGLING")
	}

	// (c) reportMissingSkillLinks-equivalent visibility (Lstat based).
	if _, err := os.Lstat(victim); err == nil {
		t.Log("STATUS WARNING: link Lstats fine, so it is NOT reported as missing")
	}

	// (d) refresh with a CHANGED fingerprint.
	SetFingerprintForTest(t, "different-fingerprint")
	res, err := Refresh()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(victim); err == nil {
		t.Log("REFRESH(changed fp): repaired")
	} else {
		t.Logf("REFRESH(changed fp): STILL DANGLING; skipped=%v failed=%v", res.Skipped, res.Failed)
	}
}

// Q1: how long does a ModeCopy-shaped install actually take here?
func TestScratchCopyCost(t *testing.T) {
	dest := t.TempDir()
	gen := filepath.Join(dest, "gen")
	start := time.Now()
	if err := copyTree(Assets(), gen); err != nil {
		t.Fatal(err)
	}
	genCost := time.Since(start)
	names, _ := Names()
	start = time.Now()
	for target := 0; target < 5; target++ {
		for _, n := range names {
			d := filepath.Join(dest, "t", string(rune('a'+target)), n)
			if err := os.MkdirAll(filepath.Dir(d), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := CopyDir(filepath.Join(gen, n), d); err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Logf("%d skills; EnsureGeneration copy=%v; 5-target ModeCopy projection=%v (local SSD, no AV)",
		len(names), genCost, time.Since(start))
}

// Q1/Q5: does the age break fire against a LIVE holder?
func TestScratchAgeBreaksLiveHolder(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".orq"), 0o755); err != nil {
		t.Fatal(err)
	}
	path, _ := lockPath()
	// A lock held by THIS process (certainly alive), aged past lockMaxAge.
	mine, _ := lockToken()
	if err := os.WriteFile(path, []byte(mine), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * lockMaxAge)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	pid, _ := lockPID(mine)
	t.Logf("holder pid %d alive=%v; lockIsStale=%v", pid, processAlive(pid), lockIsStale(path))
	rel, err := acquireLock()
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if rel != nil {
		data, _ := os.ReadFile(path)
		t.Logf("SECOND WRITER ACQUIRED alongside a live holder; lock now holds %q (was %q)", string(data), mine)
		rel()
	}
}

// Q6: does the skipped concurrency test actually reproduce?
func TestScratchStaleBreakOverlap(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".orq"), 0o755); err != nil {
		t.Fatal(err)
	}
	path, _ := lockPath()
	var inside, overlaps, acquired int64
	var wg sync.WaitGroup
	for round := 0; round < 40; round++ {
		if err := os.WriteFile(path, []byte("999999"), 0o600); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				release, err := acquireLock()
				if err != nil || release == nil {
					return
				}
				atomic.AddInt64(&acquired, 1)
				if atomic.AddInt64(&inside, 1) > 1 {
					atomic.AddInt64(&overlaps, 1)
				}
				time.Sleep(time.Millisecond)
				atomic.AddInt64(&inside, -1)
				release()
			}()
		}
		wg.Wait()
	}
	t.Logf("acquired=%d overlaps=%d (of 320 attempts)", acquired, overlaps)
}
