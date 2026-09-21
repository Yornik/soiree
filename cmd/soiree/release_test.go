package main

import (
	"encoding/json"
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
	Uses string            `yaml:"uses"`
	Run  string            `yaml:"run"`
	With map[string]any    `yaml:"with"`
	Env  map[string]string `yaml:"env"`
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

// `:latest` is what README.md hands a new reader, and SECURITY.md defines the
// supported version as "the newest `vX.Y.Z` tag, which is what
// `ghcr.io/yornik/soiree:latest` points at", so the floating tag has to name
// the newest release rather than the last one to reach the registry. Those are
// different whenever the publish order is not the release order: two releases
// cut minutes apart finish their checks in whichever order their runners take,
// a re-run of a flaked check publishes after the later release has landed, and
// a tag pushed at an old commit has nothing to race at all. Nothing reports
// it, since both runs are green and each verifies its own digest. So the tag
// list is computed by a step that compares the version being released against
// the tags on origin, and `:latest` is never in the fixed list a job hands the
// builder.
func TestTheFloatingTagFollowsTheNewestRelease(t *testing.T) {
	for name, jobs := range releaseJobs(t) {
		for id, j := range jobs {
			compares := false
			for _, s := range j.Steps {
				if strings.Contains(s.Run, "--sort=-v:refname") {
					compares = true
				}
				if strings.Contains(fmt.Sprint(s.With["tags"]), ":latest") {
					t.Errorf(".github/workflows/%s: job %q hands the builder :latest in a fixed list, so it moves the floating tag whichever version it is publishing",
						name, id)
				}
			}
			if !compares {
				t.Errorf(".github/workflows/%s: job %q publishes without comparing the version it releases against the tags on origin, so whichever release reaches the registry last owns :latest",
					name, id)
			}
		}
	}
}

// The two publishing jobs share one concurrency group, so they do not write
// the registry at the same time. That is the whole of what it buys: a job
// joins its group only once `needs` is satisfied, so the group does not make
// the publish order the release order, and a re-run of a flaked check joins it
// long after the later release has landed. Which digest `:latest` ends on is
// TestTheFloatingTagFollowsTheNewestRelease's subject. What this one keeps is
// the serialising, and the queueing rather than the replacing.
func TestTwoReleasesDoNotPublishAtOnce(t *testing.T) {
	groups := make(map[string]int)
	for name, jobs := range releaseJobs(t) {
		for id, j := range jobs {
			switch {
			case j.Concurrency.Group == "":
				t.Errorf(".github/workflows/%s: job %q publishes in no concurrency group, so two releases can be pushing and signing at the same moment",
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
		t.Errorf("the publishing jobs are spread over %d concurrency groups (%v), and a group serialises only against itself, so the two release paths can still publish at once",
			len(groups), groups)
	}
}

// A signature says which workflow in which repository built an image. It does
// not say which release the image is: release-please signs every build under
// the same `@refs/heads/main` identity, so `cosign verify` on `:vX.Y.Z`
// succeeds for any image that workflow has ever signed. Whoever can move a tag
// in the registry can point it at an older release and the documented
// verification still passes, which matters because everything below v1.0.0 is
// signed and serves `/api/v1` with no authorisation at all. The version
// annotation closes that: it is covered by the signature, and a command that
// asks for it by name gets `missing or incorrect annotation` on anything else.
// Both halves are asserted, since an annotation nothing requires is not a
// check.
func TestASignatureNamesTheVersionItSigns(t *testing.T) {
	// One spelling, shared by both workflows and by the commands in
	// docs/verifying-releases.md, so what a reader pastes is what a release
	// runs against itself.
	const annotation = `-a "version=${TAG}"`

	signed, required := 0, 0
	for name, jobs := range releaseJobs(t) {
		for id, j := range jobs {
			for _, s := range j.Steps {
				code := withoutComments(s.Run)
				signs := strings.Contains(code, "cosign sign")
				verifies := strings.Count(code, "cosign verify")
				if !signs && verifies == 0 {
					continue
				}

				// The version has to arrive from the release being cut. A
				// literal would go on naming whichever release was current on
				// the day somebody typed it, and would verify just as well.
				if tag := s.Env["TAG"]; !strings.Contains(tag, "tag_name") && !strings.Contains(tag, "ref_name") {
					t.Errorf(".github/workflows/%s: job %q runs cosign in a step whose TAG is %q, so the version in the signature is not the one being released",
						name, id, tag)
				}

				if signs {
					signed++
					if !strings.Contains(code, annotation) {
						t.Errorf(".github/workflows/%s: job %q signs without %s, so the signature names no version and a tag moved onto an older release verifies as that release",
							name, id, annotation)
					}
				}
				if verifies == 0 {
					continue
				}
				// Every verify call, not just one: the self-check exists to
				// fail a release here rather than in somebody else's admission
				// controller, and a call that drops the annotation is a
				// documented command nobody is exercising.
				if got := strings.Count(code, annotation); got < verifies {
					t.Errorf(".github/workflows/%s: job %q makes %d cosign verify calls and asks for %s in %d of them",
						name, id, verifies, annotation, got)
					continue
				}
				required++
			}
		}
	}
	if signed == 0 || required == 0 {
		t.Fatalf("read %d signing steps and %d verifying steps, so this test is not looking at the release path", signed, required)
	}
}

// withoutComments drops the comment lines from a run block. The comments in
// these steps name the commands they explain, and a comment counted as a call
// would fail the step for a sentence somebody wrote about it.
func withoutComments(run string) string {
	var code []string
	for _, line := range strings.Split(run, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "#") {
			code = append(code, line)
		}
	}
	return strings.Join(code, "\n")
}

// Renovate merges most of its own pull requests here, so for those the only
// review a new release gets is that CI compiled it, and CI cannot tell a
// benign release from a compromised or withdrawn one: the checks build with
// `push: false` and never sign, so they say nothing at all about the four
// actions that run in the two jobs above holding `packages: write` and
// `id-token: write`. The wait is the review. It is a top-level default rather
// than a rule because a rule covers the managers somebody listed, and the
// first wait here listed gomod and npm, which left the actions, the builder
// image and the tool versions the workflows pin inline free to merge
// themselves the day they were published.
func TestNoDependencyMergesItselfTheDayItIsPublished(t *testing.T) {
	// Rules are read as raw values keyed by name, because the way to switch
	// the wait off is to set it to null, which is what vulnerabilityAlerts
	// does by default, and a null decoded into any typed field is the same nil
	// as a rule that never mentioned the field at all.
	var cfg struct {
		MinimumReleaseAge json.RawMessage              `json:"minimumReleaseAge"`
		PackageRules      []map[string]json.RawMessage `json:"packageRules"`
	}
	if err := json.Unmarshal([]byte(repoFile(t, "renovate.json")), &cfg); err != nil {
		t.Fatalf("parse renovate.json: %v", err)
	}

	if len(cfg.MinimumReleaseAge) == 0 || mergesOnPublicationDay(cfg.MinimumReleaseAge) {
		t.Errorf("renovate.json sets minimumReleaseAge to %s at the top level, so an update automerges as soon as CI is green and a compromised release reaches main on the day it is published",
			orNothing(cfg.MinimumReleaseAge))
	}

	// A rule may lengthen the wait; nothing may switch it off, which is how
	// the previous one came to cover two of the managers in use.
	for i, r := range cfg.PackageRules {
		age, set := r["minimumReleaseAge"]
		if !set {
			continue
		}
		if mergesOnPublicationDay(age) {
			t.Errorf("renovate.json packageRules[%d] sets minimumReleaseAge to %s, which turns the wait off for what it matches, so those updates merge themselves unreviewed",
				i, orNothing(age))
		}
	}
}

// mergesOnPublicationDay reports whether a minimumReleaseAge value lets an
// update merge the day it was published: an explicit null, which clears an
// inherited wait, a zero, or a duration counted from zero. A duration arrives
// quoted, so the leading zero is found in the decoded string rather than in
// the raw bytes.
func mergesOnPublicationDay(age json.RawMessage) bool {
	switch strings.TrimSpace(string(age)) {
	case "null", "0":
		return true
	}
	var wait string
	if err := json.Unmarshal(age, &wait); err != nil {
		return false
	}
	return wait == "" || strings.HasPrefix(wait, "0")
}

func orNothing(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "nothing"
	}
	return string(raw)
}
