package daemon

import (
	"fmt"

	"github.com/dark-factory-build/dark-factory/internal/kernel"
	"github.com/dark-factory-build/dark-factory/internal/provider"
	"github.com/dark-factory-build/dark-factory/internal/runner"
)

// taskFetchInstruction is a native launch's task only when the complete task,
// knowledge and continuation context do not fit its prompt.
const taskFetchInstruction = `Before doing anything else, use the Factory attempt tool with argv ["attempt","task"]: the complete task, its project knowledge and continuation context did not fit in this launch.`

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

// providerTaskForContinuationLaunch frames the first candidate that fits the
// provider's launch. A native launch none fits gets taskFetchInstruction.
func providerTaskForContinuationLaunch(kind kernel.Provider, contexts []kernel.ContinuationContext, candidates ...[]byte) ([]byte, error) {
	err := provider.ErrInvalid
	for _, task := range candidates {
		framed, frameErr := providerTaskWithContinuationContext(kind, task, contexts)
		if frameErr == nil {
			if _, _, frameErr = provider.PrepareTask(kind, framed); frameErr == nil {
				return framed, nil
			}
		}
		err = frameErr
	}
	if kind != kernel.ProviderShell {
		return []byte(taskFetchInstruction), nil
	}
	return nil, err
}
