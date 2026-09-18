package daemon

import (
	"github.com/dark-factory-build/dark-factory/internal/api"
	"github.com/dark-factory-build/dark-factory/internal/browserprotocol"
	"testing"
)

func TestBrowserIntakePreservesPriorityRules(t *testing.T) {
	input := browserIntakeInput(browserprotocol.Intake{Action: "update", Configuration: &browserprotocol.IntakeConfiguration{PriorityDefault: 2, PriorityByLabel: map[string]int64{"urgent": 10}}})
	if input.Configuration.PriorityDefault != 2 || input.Configuration.PriorityByLabel["urgent"] != 10 {
		t.Fatal("browser update lost migrated priority rules")
	}
	result := browserIntakeResult(api.IntakeResult{State: "ok", Sources: []api.IntakeSource{{PriorityDefault: input.Configuration.PriorityDefault, PriorityByLabel: input.Configuration.PriorityByLabel}}})
	if len(result.Sources) != 1 || result.Sources[0].PriorityDefault != 2 || result.Sources[0].PriorityByLabel["urgent"] != 10 {
		t.Fatal("browser read lost migrated priority rules")
	}
}
