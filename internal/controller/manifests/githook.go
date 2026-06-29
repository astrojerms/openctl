package manifests

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/openctl/openctl/pkg/protocol"
)

// GitHook returns a DiskMirror hook that commits each materialize/delete
// to repo with a structured message. The source ("cli"/"ui") is pulled
// from ctx via operations.SourceFromContext; absent source falls back to
// "cli" so commits authored by old CLI clients or the pre-source dispatcher
// still come out readable.
//
// If pushAfterCommit is true (matching PushModeOnCommit), each successful
// commit is followed by a `git push`. Push failures are logged and
// swallowed — never bubbled back into the dispatcher, which would
// otherwise mark the apply as failed because of a flaky network.
//
// Nothing-to-commit (the user re-applied an identical manifest, so disk
// didn't change) is silently swallowed: ErrNothingToCommit is not an
// error from the caller's perspective.
func GitHook(repo *Repo, pushAfterCommit bool) func(ctx context.Context, kind string, r *protocol.Resource, verb string) error {
	return func(ctx context.Context, kind string, r *protocol.Resource, verb string) error {
		source := SourceFromContext(ctx)
		if source == "" {
			source = SourceCLI
		}
		msg := commitMessage(verb, kind, r.Metadata.Name, source)

		if err := repo.CommitAll(ctx, msg); err != nil {
			if errors.Is(err, ErrNothingToCommit) {
				return nil
			}
			return err
		}
		if pushAfterCommit {
			if err := repo.Push(ctx); err != nil {
				log.Printf("git push after commit: %v", err)
			}
		}
		return nil
	}
}

// commitMessage formats the per-commit one-liner. Format:
//
//	apply VirtualMachine/foo via CLI
//	delete VirtualMachine/foo via UI
func commitMessage(verb, kind, name, source string) string {
	src := strings.ToUpper(source)
	return fmt.Sprintf("%s %s/%s via %s", verb, kind, name, src)
}
