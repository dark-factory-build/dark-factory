package daemon

import (
	"fmt"

	"github.com/dark-factory-build/dark-factory/internal/kernel"
	"github.com/dark-factory-build/dark-factory/internal/provider"
)

func providerTaskWithContinuationContext(kind kernel.Provider, task []byte, contexts []kernel.ContinuationContext) ([]byte, error) {
	limit := kernel.MaxContinuationTaskBytes
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
	framed, err := providerTaskWithContinuationContext(kind, task, contexts)
	if err != nil {
		return nil, err
	}
	if kind == kernel.ProviderShell {
		if _, _, err := provider.PrepareTask(kind, framed); err != nil {
			return nil, err
		}
	}
	return framed, nil
}
