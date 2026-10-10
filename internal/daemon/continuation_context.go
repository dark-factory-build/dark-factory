package daemon

import (
	"fmt"

	"github.com/dark-factory-build/dark-factory/internal/kernel"
	"github.com/dark-factory-build/dark-factory/internal/provider"
	"github.com/dark-factory-build/dark-factory/internal/runner"
)

const continuationTaskFetchInstruction = `This is resumed work. Run "$DARK_FACTORY_FACTORYCTL" attempt task before doing anything else to read the complete original task and Factory continuation context.`

func providerTaskWithContinuationContext(kind kernel.Provider, task []byte, contexts []kernel.ContinuationContext) ([]byte, error) {
	limit := runner.MaxProviderTaskBytes
	if kind != kernel.ProviderShell {
		limit = runner.MaxNativeTaskBytes
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
	framed := kernel.ContinuationTaskText(string(task), contexts, kind == kernel.ProviderShell)
	if len(framed) > limit {
		return nil, fmt.Errorf("%w: continuation context exceeds task delivery bound", kernel.ErrInvalidValue)
	}
	return []byte(framed), nil
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
