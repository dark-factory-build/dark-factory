package daemon

import (
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

	"github.com/dark-factory-build/dark-factory/internal/kernel"
	"github.com/dark-factory-build/dark-factory/internal/provider"
	"github.com/dark-factory-build/dark-factory/internal/runner"
)

const continuationTaskFetchInstruction = `This is resumed work. Run "$DARK_FACTORY_FACTORYCTL" attempt task before doing anything else to read the complete original task and Factory continuation context.`

func providerTaskWithContinuationContext(kind kernel.Provider, task []byte, contexts []kernel.ContinuationContext) ([]byte, error) {
	limit := runner.MaxProviderTaskBytes
	if kind == kernel.ProviderCodex {
		limit = runner.MaxCodexTaskBytes
	}
	return taskWithContinuationContext(kind, task, contexts, limit)
}

func attemptTaskWithContinuationContext(kind kernel.Provider, task []byte, contexts []kernel.ContinuationContext) ([]byte, error) {
	return taskWithContinuationContext(kind, task, contexts, kernel.MaxContinuationTaskBytes)
}

func taskWithContinuationContext(kind kernel.Provider, task []byte, contexts []kernel.ContinuationContext, limit int) ([]byte, error) {
	if len(contexts) == 0 {
		return task, nil
	}
	if len(task) > limit {
		return nil, fmt.Errorf("%w: continuation context exceeds task delivery bound", kernel.ErrInvalidValue)
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
	if builder.Len() > limit {
		return nil, fmt.Errorf("%w: continuation context exceeds task delivery bound", kernel.ErrInvalidValue)
	}
	return []byte(builder.String()), nil
}

func providerTaskForContinuationLaunch(kind kernel.Provider, task []byte, contexts []kernel.ContinuationContext) ([]byte, error) {
	framed, frameErr := providerTaskWithContinuationContext(kind, task, contexts)
	if frameErr == nil {
		if _, _, err := provider.PrepareTask(kind, framed); err == nil {
			return framed, nil
		}
	}
	if len(contexts) != 0 && kind != kernel.ProviderShell {
		fallback := []byte(continuationTaskFetchInstruction)
		if _, _, err := provider.PrepareTask(kind, fallback); err == nil {
			return fallback, nil
		}
	}
	if frameErr != nil {
		return nil, frameErr
	}
	return nil, provider.ErrInvalid
}
