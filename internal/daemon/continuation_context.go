package daemon

import (
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

	"github.com/dark-factory-build/dark-factory/internal/kernel"
	"github.com/dark-factory-build/dark-factory/internal/runner"
)

func providerTaskWithContinuationContext(kind kernel.Provider, task []byte, contexts []kernel.ContinuationContext) ([]byte, error) {
	if len(contexts) == 0 {
		return task, nil
	}
	var builder strings.Builder
	builder.Grow(len(task) + len(contexts)*128)
	builder.Write(task)
	if kind == kernel.ProviderShell {
		builder.WriteString("\n\n# Factory continuation context:\n")
	} else {
		builder.WriteString("\n\nFactory continuation context:\n")
	}
	for _, continuation := range contexts {
		if kind == kernel.ProviderShell {
			builder.WriteString("# ")
		}
		builder.WriteString("condition=")
		builder.WriteString(string(continuation.ConditionKind))
		builder.WriteString(" condition_id=")
		builder.WriteString(hex.EncodeToString(continuation.ConditionID.Bytes()))
		builder.WriteString(" condition_revision=")
		builder.WriteString(strconv.FormatInt(continuation.ConditionRevision.Int64(), 10))
		builder.WriteString(" context_digest=")
		builder.WriteString(hex.EncodeToString(continuation.ContextDigest[:]))
		builder.WriteString(" resolution=")
		builder.WriteString(continuation.ResolutionDetail)
		builder.WriteByte('\n')
	}
	if builder.Len() > runner.MaxProviderTaskBytes {
		return nil, fmt.Errorf("%w: continuation context exceeds task delivery bound", kernel.ErrInvalidValue)
	}
	return []byte(builder.String()), nil
}
