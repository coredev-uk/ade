package review

import (
	"context"
	"fmt"

	"github.com/coredev-uk/ade/internal/forge"
)

// CloseMR closes the MR (forge.mr.close). The wrapped driver must implement
// forge.MRStateWriter; one that does not is an error, not a silent no-op.
func (a *AuditedWriter) CloseMR(ctx context.Context, mr forge.Ref) error {
	sw, ok := a.MRWriter.(forge.MRStateWriter)
	if !ok {
		return fmt.Errorf("this forge cannot close merge requests yet (%s)", mr)
	}
	return a.do("forge.mr.close", mr.String(), "", nil, func() error { return sw.CloseMR(ctx, mr) })
}

// ReopenMR reopens a closed MR (forge.mr.reopen); see CloseMR.
func (a *AuditedWriter) ReopenMR(ctx context.Context, mr forge.Ref) error {
	sw, ok := a.MRWriter.(forge.MRStateWriter)
	if !ok {
		return fmt.Errorf("this forge cannot reopen merge requests yet (%s)", mr)
	}
	return a.do("forge.mr.reopen", mr.String(), "", nil, func() error { return sw.ReopenMR(ctx, mr) })
}
