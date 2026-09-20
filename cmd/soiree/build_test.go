package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// How this binary is built is described in files the compiler never reads, so
// the promises those files make about each other — that CI tests the toolchain
// the image ships, that nothing installs a version nobody chose — are checked
// here or nowhere.

// builderStage matches the build stage's FROM line, whatever it names, so a
// reference that has stopped being a pin still reaches the assertions below
// rather than silently failing to match.
var builderStage = regexp.MustCompile(`(?m)^FROM golang:(\S+) AS build\b`)

// goVersionEnv matches the toolchain the CI jobs hand to setup-go.
var goVersionEnv = regexp.MustCompile(`(?m)^\s+GO_VERSION: '([^']+)'`)

// patchTag matches a tag that names one Go release rather than a line of them.
var patchTag = regexp.MustCompile(`^\d+\.\d+\.\d+(-|$)`)

// sha256Digest matches a digest in the form a registry hands back.
var sha256Digest = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// leadingVersion takes the version off a tag, so `1.27.1-alpine` yields what
// setup-go would have to be given to match it.
var leadingVersion = regexp.MustCompile(`^\d+(\.\d+)*`)

// toolchainAsk matches the two lines in go.mod that can send a build looking
// for a newer Go than the one it was started with: the `go` directive and an
// explicit `toolchain` line.
var toolchainAsk = regexp.MustCompile(`(?m)^(?:go|toolchain go)\s*(\d\S*)$`)

// leadingDigits matches the number at the front of a version component, so a
// prerelease such as `1.28rc1` still compares as 1.28.
var leadingDigits = regexp.MustCompile(`^\d+`)

// floatingTool matches the two forms a tool version takes in these workflows:
// `@latest` after a module path, and `latest` as an action input. Neither
// matches `ubuntu-latest` or the `:latest` image tag, which are names rather
// than versions of anything installed.
var floatingTool = regexp.MustCompile(`@latest|version:\s*['"]?latest['"]?`)

// repoFile reads a file from the repository root by the path a person would
// write.
func repoFile(t *testing.T, name string) string {
	t.Helper()

	raw, err := os.ReadFile(filepath.Join("..", "..", filepath.FromSlash(name)))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(raw)
}

// The Dockerfile was the one thing in this pipeline still free to change under
// a release. Every action is pinned to a commit digest because a moving tag is
// a supply-chain hole, and then the image that compiles the binary those
// actions sign named a line of releases: `golang:1.27-alpine` resolves to
// whichever patch shipped last, and go1.27.0 and go1.27.1 produce different
// bytes from the same source. GO_VERSION had the same shape, so "CI tests the
// same toolchain the image ships" held only as far as the minor.
//
// Each is a claim about files agreeing, and until now nothing read them.
func TestTheBuilderIsPinnedAndCITestsThatSameToolchain(t *testing.T) {
	m := builderStage.FindStringSubmatch(repoFile(t, "Dockerfile"))
	if m == nil {
		t.Fatal("the Dockerfile has no `FROM golang:... AS build` stage, so this test no longer knows what it is reading")
	}

	tag, digest, pinned := strings.Cut(m[1], "@")
	switch {
	case !pinned:
		t.Errorf("the builder image is %q and carries no digest, so the bytes it compiles with can change under a release", m[1])
	case !sha256Digest.MatchString(digest):
		t.Errorf("the builder image digest is %q, which is not a sha256 digest", digest)
	}
	if !patchTag.MatchString(tag) {
		t.Errorf("the builder image is tagged %q, which names a line of Go releases rather than one of them", tag)
	}

	pinnedGo := leadingVersion.FindString(tag)

	v := goVersionEnv.FindStringSubmatch(repoFile(t, ".github/workflows/ci.yaml"))
	if v == nil {
		t.Fatal("ci.yaml declares no GO_VERSION, so nothing says which toolchain the jobs test with")
	}
	if v[1] != pinnedGo {
		t.Errorf("CI tests with Go %q and the image ships Go %q; ci.yaml asks for those to be the same", v[1], pinnedGo)
	}

	// The digest fixes which Go the image carries, not which Go compiles.
	// GOTOOLCHAIN is left at its default everywhere, so a go.mod asking for
	// more than the image carries sends the build off to download another
	// toolchain, and the pin decides nothing. No job would notice, since they
	// would all download the same one. Nor is the ask something a person here
	// chooses: a2fe2b0, an automerged dependency bump, moved the `go`
	// directive from 1.25.0 to 1.26.0 on its own.
	asks := toolchainAsk.FindAllStringSubmatch(repoFile(t, "go.mod"), -1)
	if asks == nil {
		t.Fatal("go.mod names no Go version, so nothing says which toolchain this module asks for")
	}
	for _, ask := range asks {
		if compareGoVersions(ask[1], pinnedGo) > 0 {
			t.Errorf("go.mod asks for Go %q and the pinned builder carries Go %q, so a build would fetch a toolchain no digest names", ask[1], pinnedGo)
		}
	}
}

// compareGoVersions orders two Go versions by their numbers rather than their
// text, which a plain string comparison gets wrong the moment a component
// reaches two digits: 1.9 reads as the later release and is the earlier one.
func compareGoVersions(a, b string) int {
	x, y := goVersionNumbers(a), goVersionNumbers(b)
	for i := 0; i < len(x) || i < len(y); i++ {
		var l, r int
		if i < len(x) {
			l = x[i]
		}
		if i < len(y) {
			r = y[i]
		}
		switch {
		case l < r:
			return -1
		case l > r:
			return 1
		}
	}
	return 0
}

// goVersionNumbers reads a version as the numbers a person compares, so that a
// two-part `go 1.26` and a three-part 1.26.0 come out equal.
func goVersionNumbers(v string) []int {
	var out []int
	for _, part := range strings.Split(v, ".") {
		digits := leadingDigits.FindString(part)
		if digits == "" {
			break
		}
		n, err := strconv.Atoi(digits)
		if err != nil {
			break
		}
		out = append(out, n)
	}
	return out
}

// The rule is stated in ci.yaml beside the redocly pin: "Pinned rather than
// @latest, like every action in this file: a floating version changes CI with
// no commit, and Renovate cannot track it." It was then not applied to the two
// tools installed further down the same file. golangci-lint runs its default
// linter set here, since the repository has no configuration for it, so a
// release that adds a linter to that set turns main red with nothing of ours
// having changed — and every automerge rule in renovate.json waits for green
// checks, so the dependency updates stop along with it.
func TestNoWorkflowInstallsAFloatingToolVersion(t *testing.T) {
	dir := filepath.Join("..", "..", ".github", "workflows")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}

	read := 0
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml") {
			continue
		}
		read++

		for i, line := range strings.Split(repoFile(t, ".github/workflows/"+name), "\n") {
			// Up to the comment only: the rule itself is written in one, and
			// prose about `@latest` is not a step that installs it.
			step, _, _ := strings.Cut(line, "#")
			if floatingTool.MatchString(step) {
				t.Errorf(".github/workflows/%s:%d installs whatever version is current: %s",
					name, i+1, strings.TrimSpace(line))
			}
		}
	}
	if read == 0 {
		t.Fatal("no workflow files were read, so this test asserted nothing")
	}
}
