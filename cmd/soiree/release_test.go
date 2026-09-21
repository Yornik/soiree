package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	yaml "go.yaml.in/yaml/v3"
)

// Two jobs put a release into the registry, one for a tag pushed by hand and
// one for a release release-please cut, and what keeps a red commit out of
// both is a `needs` edge in a file nothing else reads. v1.1.0 and v1.2.1 went
// out without one: each was built, pushed as `:latest` and signed while the
// same commit's run was red, because the publishing job waited only for the
// job that made the tag. The edge is cheap to delete by accident and there is
// no ruleset behind it, so it is asserted here.

// checksWorkflow holds the jobs a release has to pass. A publishing job
// reaches it through `needs`, directly or through another job.
const checksWorkflow = ".github/workflows/checks.yaml"

type workflowFile struct {
	Jobs map[string]workflowJob `yaml:"jobs"`
}

type workflowJob struct {
	Needs       jobNames         `yaml:"needs"`
	If          string           `yaml:"if"`
	Uses        string           `yaml:"uses"`
	Concurrency concurrencyGroup `yaml:"concurrency"`
	Steps       []workflowStep   `yaml:"steps"`
}

type workflowStep struct {
	Uses string         `yaml:"uses"`
	Run  string         `yaml:"run"`
	With map[string]any `yaml:"with"`
}

// jobNames reads `needs:` in either form GitHub accepts: one job name, or a
// list of them.
type jobNames []string

func (n *jobNames) UnmarshalYAML(value *yaml.Node) error {
	var one string
	if err := value.Decode(&one); err == nil {
		*n = jobNames{one}
		return nil
	}
	var many []string
	if err := value.Decode(&many); err != nil {
		return err
	}
	*n = many
	return nil
}

// concurrencyGroup reads `concurrency:` in either form GitHub accepts: the
// group name on its own, or a mapping that also says what happens to a run
// already in flight.
type concurrencyGroup struct {
	Group            string `yaml:"group"`
	CancelInProgress any    `yaml:"cancel-in-progress"`
}

func (c *concurrencyGroup) UnmarshalYAML(value *yaml.Node) error {
	var name string
	if err := value.Decode(&name); err == nil {
		c.Group = name
		return nil
	}
	// A named type, so decoding the mapping does not call this method again.
	type mapping concurrencyGroup
	var m mapping
	if err := value.Decode(&m); err != nil {
		return err
	}
	*c = concurrencyGroup(m)
	return nil
}

// workflows parses every workflow in the repository, since which file holds
// the release path is exactly the sort of thing a refactor moves.
func workflows(t *testing.T) map[string]workflowFile {
	t.Helper()

	dir := filepath.Join("..", "..", ".github", "workflows")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}

	out := make(map[string]workflowFile)
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml") {
			continue
		}
		var w workflowFile
		if err := yaml.Unmarshal([]byte(repoFile(t, ".github/workflows/"+name)), &w); err != nil {
			t.Fatalf("parse .github/workflows/%s: %v", name, err)
		}
		out[name] = w
	}
	return out
}

// publishes reports whether a job hands an image to the world: a push moves
// `:latest`, and a signature is what a third party reads instead of the code.
// The image the validation job builds and throws away is not one of those.
func publishes(j workflowJob) bool {
	for _, s := range j.Steps {
		if strings.HasPrefix(s.Uses, "docker/build-push-action") && truthy(s.With["push"]) {
			return true
		}
		if strings.Contains(s.Run, "cosign sign") {
			return true
		}
	}
	return false
}

// truthy reads an action input written either as a YAML boolean or as the
// string an expression would have left behind.
func truthy(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		return t == "true"
	}
	return false
}

// waitsForTheChecks follows `needs` from one job to the job that calls the
// shared checks, however many jobs apart the two are.
func waitsForTheChecks(w workflowFile, id string, seen map[string]bool) bool {
	if seen[id] {
		return false
	}
	seen[id] = true

	for _, need := range w.Jobs[id].Needs {
		if strings.Contains(w.Jobs[need].Uses, checksWorkflow) {
			return true
		}
		if waitsForTheChecks(w, need, seen) {
			return true
		}
	}
	return false
}

// releaseJobs collects the publishing jobs, and refuses to report none: a
// renamed action or input would otherwise leave the tests below passing while
// they read nothing at all.
func releaseJobs(t *testing.T) map[string]map[string]workflowJob {
	t.Helper()

	found := make(map[string]map[string]workflowJob)
	count := 0
	for name, w := range workflows(t) {
		for id, j := range w.Jobs {
			if !publishes(j) {
				continue
			}
			if found[name] == nil {
				found[name] = make(map[string]workflowJob)
			}
			found[name][id] = j
			count++
		}
	}
	if count < 2 {
		t.Fatalf("found %d jobs that publish a release image, and there are two release paths, so this test is reading the wrong thing", count)
	}
	return found
}

func TestNothingIsPushedOrSignedWithoutTheChecks(t *testing.T) {
	parsed := workflows(t)

	for name, jobs := range releaseJobs(t) {
		for id := range jobs {
			if !waitsForTheChecks(parsed[name], id, map[string]bool{}) {
				t.Errorf(".github/workflows/%s: job %q pushes and signs a release image without waiting for %s, so a commit whose tests never ran can go out as :latest",
					name, id, checksWorkflow)
			}
		}
	}
}

// The signed artifact is built cold. Every job that runs on main can write the
// shared Actions cache, including the ones that execute dependencies, so a
// layer restored from it is a way for code that is not in this repository to
// end up inside a signed image, which SECURITY.md puts in scope. What the
// cache saves here is the module download: the source and the version stamp
// change on every release, so the compile layer never hits it anyway.
func TestASignedImageIsNotBuiltFromTheSharedCache(t *testing.T) {
	for name, jobs := range releaseJobs(t) {
		for id, j := range jobs {
			for _, s := range j.Steps {
				for _, key := range []string{"cache-from", "cache-to"} {
					if _, ok := s.With[key]; ok {
						t.Errorf(".github/workflows/%s: job %q signs what it builds and sets %s, so the image can carry a layer another job on main put in the cache",
							name, id, key)
					}
				}
			}
		}
	}
}

// A green `checks` says the tagged tree passes its own tests. It says nothing
// about how that tree got there: `v*` is not restricted to commits on main and
// no ruleset stands behind the tag either, so a tag pushed at a branch that
// never opened a pull request would be built, pushed as `:latest` and signed
// with the identity docs/verifying-releases.md tells a third party to trust.
// The other release path builds what main has just merged, so the assertion is
// scoped to the job a tag triggers.
func TestATagOffMainIsNotReleased(t *testing.T) {
	gated := 0
	for name, jobs := range releaseJobs(t) {
		for id, j := range jobs {
			if !strings.Contains(j.If, "refs/tags/") {
				continue
			}
			gated++

			ancestry, depth := false, "unset"
			for _, s := range j.Steps {
				if strings.Contains(s.Run, "merge-base --is-ancestor") {
					ancestry = true
				}
				if strings.HasPrefix(s.Uses, "actions/checkout") {
					depth = fmt.Sprint(s.With["fetch-depth"])
				}
			}

			if !ancestry {
				t.Errorf(".github/workflows/%s: job %q publishes a tag without checking that its commit is reachable from main, so a tag pushed at any commit in the repository goes out as a signed :latest",
					name, id)
			}
			// The check compares two histories and the default checkout is one
			// commit deep, which shares none of either. Losing the depth would
			// not weaken the guard, it would fail every release at the moment
			// it was cut, so it is asserted next to the check it serves.
			if depth != "0" {
				t.Errorf(".github/workflows/%s: job %q checks the tag against main with fetch-depth %s; the two histories have to be present for merge-base to answer",
					name, id, depth)
			}
		}
	}
	if gated == 0 {
		t.Fatal("no publishing job is gated on a tag ref, so this test read nothing")
	}
}

// Both publishing jobs write `:latest` and nothing orders them. Two releases
// cut close together, which is how this project releases, run their checks in
// parallel and finish in whichever order the runners take, so the floating tag
// can end up on the earlier digest while the release notes name the later one.
// Both runs are green and both signatures verify, since each verifies its own
// digest, so nothing reports it: the symptom is what somebody who followed
// README.md and pulled `:latest` is running, which SECURITY.md defines as the
// supported version.
func TestTwoReleasesDoNotRaceForTheFloatingTag(t *testing.T) {
	groups := make(map[string]int)
	for name, jobs := range releaseJobs(t) {
		for id, j := range jobs {
			switch {
			case j.Concurrency.Group == "":
				t.Errorf(".github/workflows/%s: job %q publishes :latest in no concurrency group, so a release that started later can overtake it and leave the floating tag on the older digest",
					name, id)
			case strings.Contains(j.Concurrency.Group, "${{"):
				t.Errorf(".github/workflows/%s: job %q is in concurrency group %q, which expands to something different in every run, so it serialises nothing",
					name, id, j.Concurrency.Group)
			default:
				groups[j.Concurrency.Group]++
			}
			// The queue has to wait rather than replace: cancelling a release
			// between the push and the signature leaves an unsigned image in
			// the registry under a tag that says it is a release.
			if truthy(j.Concurrency.CancelInProgress) {
				t.Errorf(".github/workflows/%s: job %q cancels a release that is already publishing, which can stop it between pushing the image and signing it",
					name, id)
			}
		}
	}
	// Groups are repository-wide, so one name shared by both files is what
	// makes a hand-pushed tag and a release-please release wait for each other
	// rather than each only for itself.
	if len(groups) > 1 {
		t.Errorf("the publishing jobs are spread over %d concurrency groups (%v), and a group serialises only against itself, so the two release paths still race",
			len(groups), groups)
	}
}
