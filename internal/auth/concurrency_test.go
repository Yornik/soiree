package auth

import (
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

// testParams is cheap on purpose: these tests are about waiting, not about
// cost, and the real parameters would spend their time hashing.
var testParams = Params{Memory: 64, Time: 1, Threads: 1, KeyLen: 32, SaltLen: 16}

// A login costs 19 MiB for as long as it is being checked, and anybody may ask
// for one. The bound on how many run at once is what keeps twenty parallel
// logins from being 415 MiB, so what is pinned here is that hashing really
// does wait its turn: with every slot taken, a verification does not finish,
// and with one released, it does.
func TestHashingWaitsForASlot(t *testing.T) {
	encoded, err := testParams.Hash("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}

	for i := 0; i < MaxConcurrentHashes; i++ {
		hashSlots <- struct{}{}
	}
	released := false
	defer func() {
		if !released {
			for i := 0; i < MaxConcurrentHashes; i++ {
				<-hashSlots
			}
		}
	}()

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _, _ = testParams.Verify(encoded, "a wrong guess, as most are")
	}()

	// At these parameters a verification takes well under a millisecond, so
	// this is hundreds of times longer than it needs if nothing is holding it.
	select {
	case <-done:
		t.Fatal("a verification ran while every slot was taken: something hashes outside the bound")
	case <-time.After(300 * time.Millisecond):
	}

	<-hashSlots
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a verification did not run once a slot was free")
	}
	for i := 0; i < MaxConcurrentHashes-1; i++ {
		<-hashSlots
	}
	released = true
}

// The bound only holds if everything goes through it. argon2.IDKey called
// directly, anywhere in this package, is memory spent outside the limit, and
// nothing about it would fail until the pod was killed for it.
func TestArgon2IsCalledInOnePlace(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	call := regexp.MustCompile(`argon2\.IDKey\(`)
	sites := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		src, err := os.ReadFile(e.Name())
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(string(src), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "//") {
				continue
			}
			sites += len(call.FindAllString(line, -1))
		}
	}
	if sites != 1 {
		t.Errorf("argon2.IDKey is called in %d places; it must be called in exactly one, idKey, which is what bounds it", sites)
	}
}
