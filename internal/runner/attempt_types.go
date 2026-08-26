package runner

import "time"

// AttemptStage is the closed sequence of daemon-authorized releases. Selection
// is the inner gate activation; every later release crosses the already-running
// wrapper control capability.
type AttemptStage string

const (
	StageSelection   AttemptStage = "selection"
	StagePreparation AttemptStage = "preparation"
	StagePopulation  AttemptStage = "population"
	StageProvider    AttemptStage = "provider"
)

// AttemptSpec freezes the source wrapper launch before the attempt runner is
// released. Wrapper must not contain a caller-supplied control descriptor: the
// attempt runner creates and owns that one fixed capability itself.
type AttemptSpec struct {
	AttemptID    string
	Wrapper      *LaunchSpec
	MarkerName   string
	TerminalName string
}

type AttemptEventKind string

const (
	AttemptInnerReady AttemptEventKind = "inner-ready"
	AttemptCheckpoint AttemptEventKind = "checkpoint"
	AttemptTerminal   AttemptEventKind = "terminal"
)

type AttemptEvent struct {
	Kind     AttemptEventKind
	Stage    AttemptStage
	Identity Identity
	Payload  []byte
	Terminal *TerminalRecord
}

const attemptControlTimeout = 4 * time.Second
