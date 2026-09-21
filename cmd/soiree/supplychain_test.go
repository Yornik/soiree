package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// What the published image says about itself, which licences travel inside it,
// and whether anything is still asking after it once it is deployed are all
// written in files the compiler never reads. The image is the artifact people
// run, so those promises are checked here or nowhere.

// lastStage matches the FROM line that opens the stage the registry receives.
var lastStage = regexp.MustCompile(`(?m)^FROM \S+(?: AS \S+)?\s*$`)

// ociLabel matches one `org.opencontainers.image.<key>="<value>"` assignment.
var ociLabel = regexp.MustCompile(`org\.opencontainers\.image\.([a-z]+)="([^"]*)"`)

// copySource matches a COPY line's source and its destination. A `--from=`
// copy matches as well, and its source is a path inside an earlier stage,
// which is never one of the names the licence test looks for.
var copySource = regexp.MustCompile(`(?m)^COPY\s+(?:--\S+\s+)*(\S+)\s+(\S+)\s*$`)

// publishedStage returns the Dockerfile from its last FROM on: the stage that
// becomes the image, as opposed to the builder that is thrown away.
func publishedStage(t *testing.T) string {
	t.Helper()

	dockerfile := repoFile(t, "Dockerfile")
	found := lastStage.FindAllStringIndex(dockerfile, -1)
	if found == nil {
		t.Fatal("the Dockerfile has no FROM line, so this test no longer knows what it is reading")
	}
	return dockerfile[found[len(found)-1][0]:]
}

// workflowSources returns every workflow as text, for the promises that are
// made in a shell script rather than in a field a parser would reach.
func workflowSources(t *testing.T) map[string]string {
	t.Helper()

	dir := filepath.Join("..", "..", ".github", "workflows")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}

	out := make(map[string]string)
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml") {
			continue
		}
		out[name] = repoFile(t, ".github/workflows/"+name)
	}
	if len(out) == 0 {
		t.Fatal("no workflow files were read, so this test asserted nothing")
	}
	return out
}

// A pod carries no history. Without a source label nothing that reads the
// registry — Renovate looking for the release notes behind an image bump, a
// person running `docker inspect` on what is deployed — can get from the image
// back to the commit it was built from, and the SBOM cannot help: the build
// context excludes .git and -trimpath drops the ldflags from the build info,
// so the release asset names soiree's own version UNKNOWN. The labels are the
// one place that version survives.
func TestThePublishedImageSaysWhereItCameFrom(t *testing.T) {
	stage := publishedStage(t)

	// An ARG does not cross a FROM. Both are declared in the build stage, and
	// a label that uses one without declaring it again here does not fail: it
	// expands to an empty string and the image is labelled with nothing.
	for _, arg := range []string{"VERSION", "COMMIT"} {
		declared := regexp.MustCompile(`(?m)^ARG\s+` + arg + `\b`)
		if !declared.MatchString(stage) {
			t.Errorf("the published stage does not declare ARG %s, so a label written with ${%s} expands to an empty string", arg, arg)
		}
	}

	labels := make(map[string]string)
	for _, m := range ociLabel.FindAllStringSubmatch(stage, -1) {
		labels[m[1]] = m[2]
	}

	// Fixed text for the three that never change, and the build arguments for
	// the two that change every release: a version written by hand is a
	// version that is one release behind.
	want := map[string]string{
		"source":   "https://github.com/Yornik/soiree",
		"title":    "soiree",
		"licenses": "MIT AND OFL-1.1",
		"version":  "${VERSION}",
		"revision": "${COMMIT}",
	}
	for key, value := range want {
		got, ok := labels[key]
		if !ok {
			t.Errorf("the published image carries no org.opencontainers.image.%s label, so what it is and where it came from can be read only by someone who already knows", key)
			continue
		}
		if got != value {
			t.Errorf("org.opencontainers.image.%s is %q and should be %q", key, got, value)
		}
	}
}

// The image redistributes this project under MIT and a font under OFL-1.1, and
// the OFL asks for its notice to travel with every copy. A copy of the image
// is a copy of the font, and whoever holds one has no way back to this
// repository — /licenses is where the convention puts the texts so that they
// arrive with the thing they cover.
func TestThePublishedImageCarriesTheLicencesItRedistributes(t *testing.T) {
	stage := publishedStage(t)

	copies := make(map[string]string)
	for _, m := range copySource.FindAllStringSubmatch(stage, -1) {
		copies[m[1]] = m[2]
	}

	for _, src := range []string{"LICENSE", "LICENSES/"} {
		dst, ok := copies[src]
		if !ok {
			t.Errorf("the published stage does not copy %s, so the image redistributes work whose licence text is only in this repository", src)
			continue
		}
		if !strings.HasPrefix(dst, "/licenses") {
			t.Errorf("%s is copied to %q; /licenses is where a reader and a scanner look for it", src, dst)
		}
		if _, err := os.Stat(filepath.Join("..", "..", filepath.FromSlash(strings.TrimSuffix(src, "/")))); err != nil {
			t.Errorf("the Dockerfile copies %s into the image and the repository has no such path: %v", src, err)
		}
	}
}
