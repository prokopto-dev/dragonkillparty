package specgate

import (
	"encoding/json"
	"fmt"
	"os/exec"
)

// readBaseSpec returns the spec at baseRef, or nil when the rename check cannot or need not run.
//
// Every "nil" path below records why, and the choice between a note and a violation is the whole
// point of this function: a check that silently stopped running is worse than a check that fails.
//
//   - baseRef empty              note. The fixture switch; see EnvBaseRef.
//   - git not on PATH            VIOLATION. Nothing else here can be trusted either.
//   - baseRef resolves, no spec  note. The normal state of the PR that first commits a spec.
//   - baseRef does not resolve   VIOLATION. A shallow clone. Passing silently on one would make this
//     gate vacuous in exactly the CI configuration most likely to have it.
//   - the blob is not JSON       VIOLATION.
//
// git is invoked with root as its working directory rather than by changing this process's, so the
// unit tests can run in parallel against different fixture trees.
func readBaseSpec(rep *report, root, baseRef string) map[string]any {
	if baseRef == "" {
		rep.note("%s is empty; the operationId-rename check is disabled.", EnvBaseRef)

		return nil
	}

	if _, err := exec.LookPath("git"); err != nil {
		rep.violation("SPEC003", "git is not on PATH, so the operationId-rename check cannot run.")

		return nil
	}

	if _, err := git(root, "cat-file", "-e", baseRef+":"+SpecFile); err != nil {
		// Distinguish "the base revision has no spec yet" from "the base revision is not here".
		if _, err := git(root, "rev-parse", "--verify", "--quiet", baseRef); err != nil {
			rep.violation("SPEC003",
				"%s is not available, so no operationId could be compared against it. In CI the "+
					"spec-drift job needs `fetch-depth: 0`; locally run `git fetch origin main`. Set "+
					"%s to compare against another revision.", baseRef, EnvBaseRef)

			return nil
		}

		rep.note("%s has no %s; this is the change that introduces it.", baseRef, SpecFile)

		return nil
	}

	blob, err := git(root, "show", baseRef+":"+SpecFile)
	if err != nil {
		rep.violation("SPEC003", "could not read %s at %s.", SpecFile, baseRef)

		return nil
	}

	var doc map[string]any
	if err := json.Unmarshal(blob, &doc); err != nil {
		rep.violation("SPEC003", "%s at %s is not valid JSON: %v", SpecFile, baseRef, err)

		return nil
	}

	return doc
}

// git runs one git command in root and returns its standard output.
//
// Standard error is discarded: every caller either has a better message of its own or is probing, and
// git's own "fatal: Not a valid object name" is noise in front of the sentence this gate wants to say.
func git(root string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = root

	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git %v in %s: %w", args, root, err)
	}

	return out, nil
}
