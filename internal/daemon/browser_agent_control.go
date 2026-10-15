package daemon

import (
	"context"
	"errors"

	"github.com/dark-factory-build/dark-factory/internal/browser"
	"github.com/dark-factory-build/dark-factory/internal/browserprotocol"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
	"github.com/dark-factory-build/dark-factory/internal/provider"
)

var _ browser.AgentControlBackend = (*browserBackend)(nil)

func (backend *browserBackend) ControlAgent(ctx context.Context, principal browser.Principal, request browserprotocol.AgentControl) (browserprotocol.AgentControlResult, error) {
	operation, opErr := decodeID(request.OperationID, kernel.TaskInterventionIDFromBytes)
	task, taskErr := decodeID(request.TaskID, kernel.TaskIDFromBytes)
	run, runErr := decodeID(request.RunID, kernel.RunIDFromBytes)
	taskRevision, taskRevErr := browserDecimal(request.ExpectedTaskRevision)
	runRevision, runRevErr := browserDecimal(request.ExpectedRunRevision)
	if errors.Join(opErr, taskErr, runErr, taskRevErr, runRevErr) != nil {
		return browserprotocol.AgentControlResult{}, browser.ErrStale
	}
	spec := kernel.TaskInterventionRequest{OperationID: operation, TaskID: task, RunID: run, ExpectedTaskRevision: taskRevision, ExpectedRunRevision: runRevision}
	switch request.Action {
	case "message":
		spec.Kind = kernel.TaskInterventionMessage
		spec.Payload = request.Instruction
	case "interrupt":
		spec.Kind = kernel.TaskInterventionInterrupt
	case "stop":
		spec.Kind = kernel.TaskInterventionStop
	case "replace":
		spec.Kind = kernel.TaskInterventionReplace
	default:
		return browserprotocol.AgentControlResult{}, browser.ErrStale
	}
	var receipt kernel.TaskIntervention
	var err error
	if spec.Kind == kernel.TaskInterventionMessage || spec.Kind == kernel.TaskInterventionInterrupt {
		receipt, err = backend.owner.browserIntervention(ctx, principal, spec)
	} else {
		clientID, release, authErr := backend.authorizePrincipal(ctx, principal, kernel.BrowserCapabilityHumanActions)
		if authErr != nil {
			return browserprotocol.AgentControlResult{}, authErr
		}
		defer release()
		var successor *kernel.NewTask
		if spec.Kind == kernel.TaskInterventionReplace {
			id, idErr := decodeID(request.SuccessorTaskID, kernel.TaskIDFromBytes)
			incarnation, incErr := decodeID(request.SuccessorIncarnationID, kernel.IncarnationIDFromBytes)
			if errors.Join(idErr, incErr) != nil {
				return browserprotocol.AgentControlResult{}, browser.ErrStale
			}
			current, found, readErr := backend.store.Task(ctx, task)
			if readErr != nil {
				return browserprotocol.AgentControlResult{}, readErr
			}
			if !found {
				return browserprotocol.AgentControlResult{}, browser.ErrNotFound
			}
			if err := backend.prepareAgentInstruction(ctx, current.AssignedAgentID, request.Instruction); err != nil {
				return browserprotocol.AgentControlResult{}, err
			}
			successor = &kernel.NewTask{ID: id, IncarnationID: incarnation, Body: request.Instruction}
		}
		at, timeErr := backend.timestamp()
		if timeErr != nil {
			return browserprotocol.AgentControlResult{}, timeErr
		}
		receipt, err = backend.store.StopRunForBrowser(ctx, clientID, spec, successor, at)
	}
	if err != nil {
		return browserprotocol.AgentControlResult{}, err
	}
	backend.owner.notifyScheduler()
	result := browserprotocol.AgentControlResult{OperationID: request.OperationID, TaskID: request.TaskID, RunID: request.RunID}
	switch receipt.State {
	case kernel.TaskInterventionDelivered:
		result.Status = "delivered"
		if spec.Kind == kernel.TaskInterventionStop {
			result.Status = "stopping"
		}
		if spec.Kind == kernel.TaskInterventionReplace {
			result.Status = "queued"
			result.SuccessorTaskID = receipt.SuccessorTaskID.String()
		}
	case kernel.TaskInterventionRejected:
		result.Status = "rejected"
	default:
		result.Status = "delivery_unknown"
	}
	return result, nil
}

// taskDetailTextChunk pages task text for factoryctl task read.
func taskDetailTextChunk(value string, offset uint64) (string, bool) {
	start := taskDetailRuneOffset(value, offset)
	end := taskDetailRuneOffset(value[start:], 2048) + start
	return value[start:end], end < len(value)
}

func taskDetailRuneOffset(value string, count uint64) int {
	for offset := range value {
		if count == 0 {
			return offset
		}
		count--
	}
	return len(value)
}

func (backend *browserBackend) prepareAgentInstruction(ctx context.Context, agentID kernel.AgentID, instruction string) error {
	agent, found, err := backend.store.Agent(ctx, agentID)
	if err != nil {
		return err
	}
	if !found {
		return browser.ErrNotFound
	}
	if _, _, err := provider.PrepareTask(agent.Provider, []byte(instruction)); err != nil {
		return browser.ErrTooLarge
	}
	return nil
}
