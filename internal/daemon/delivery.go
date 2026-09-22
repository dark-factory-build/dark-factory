package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/dark-factory-build/dark-factory/internal/api"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

var deliverySHA = regexp.MustCompile(`^[0-9a-f]{40}$`)

type deliveryReceipt struct {
	State        string `json:"state"`
	SHA          string `json:"sha"`
	PR           int    `json:"pr"`
	Verification struct {
		Healthy bool   `json:"healthy"`
		SHA     string `json:"sha"`
	} `json:"verification"`
	Mode    string           `json:"delivery_mode"`
	Sources []deliverySource `json:"delivery_sources"`
}
type deliverySource struct {
	PR         int    `json:"pr"`
	Issue      int    `json:"issue"`
	MergeSHA   string `json:"merge_sha"`
	Reference  string `json:"reference"`
	Repository string `json:"repository"`
}

func (daemon *Daemon) reconcileDelivery(ctx context.Context, input api.DeliveryInput) (api.DeliveryResult, error) {
	if input.Repository != input.Release.Repository {
		return api.DeliveryResult{}, kernel.ErrInvalidValue
	}
	var envelope map[string]json.RawMessage
	var receipt deliveryReceipt
	if json.Unmarshal(input.Receipt, &envelope) != nil || string(envelope["delivery_sources"]) == "" || string(envelope["delivery_sources"]) == "null" || envelope["delivery_sources"][0] != '[' || json.Unmarshal(input.Receipt, &receipt) != nil || receipt.State != "verified" || receipt.PR < 1 || !deliverySHA.MatchString(receipt.SHA) || !receipt.Verification.Healthy || receipt.Verification.SHA != receipt.SHA || (receipt.Mode != "range" && receipt.Mode != "baseline_current" && receipt.Mode != "unchanged" && receipt.Mode != "nonancestor_baseline") {
		return api.DeliveryResult{}, kernel.ErrInvalidValue
	}
	if receipt.Mode == "unchanged" {
		return api.DeliveryResult{TaskIDs: []string{}}, nil
	}
	if receipt.Mode != "range" && len(receipt.Sources) != 0 {
		return api.DeliveryResult{}, kernel.ErrInvalidValue
	}
	project, err := parseProjectID(input.ProjectID)
	if err != nil {
		return api.DeliveryResult{}, err
	}
	agent, err := parseAgentID(input.OverseerAgentID)
	if err != nil {
		return api.DeliveryResult{}, err
	}
	at, err := daemon.timestamp()
	if err != nil {
		return api.DeliveryResult{}, err
	}
	result := api.DeliveryResult{TaskIDs: []string{}}
	for _, source := range receipt.Sources {
		if source.PR < 1 || source.Issue < 1 || !deliverySHA.MatchString(source.MergeSHA) || (source.Reference != "refs" && source.Reference != "closes") {
			return api.DeliveryResult{}, kernel.ErrInvalidValue
		}
		repository := source.Repository
		if repository == "" {
			repository = input.Repository
		}
		if !strings.EqualFold(repository, input.Repository) {
			continue
		}
		id := deliveryTaskID("delivery", input.ProjectID, input.Repository, fmt.Sprint(source.PR), fmt.Sprint(source.Issue), receipt.SHA)
		incarnation := deliveryIncarnationID("incarnation", id.String())
		title := fmt.Sprintf("Verify delivery completion for PR #%d and issue #%d", source.PR, source.Issue)
		body := fmt.Sprintf("The operator-owned release controller verified deployment of %s PR #%d at %s. The PR explicitly linked source issue #%d with %s #%d. Its live probe reported the exact SHA healthy. Read your publication journal and source-linked task outcomes. Close source issue #%d through the Maintainer App only if all acceptance criteria are satisfied; report the deployment evidence and any remaining work. Do not redeploy. This receipt does not grant additional scope.", input.Repository, source.PR, receipt.SHA, source.Issue, strings.Title(source.Reference), source.Issue, source.Issue)
		if _, found, readErr := daemon.store.TaskRecovery(ctx, id, incarnation); readErr != nil {
			return api.DeliveryResult{}, readErr
		} else if !found {
			if _, enqueueErr := daemon.store.EnqueueTask(ctx, kernel.NewTask{ID: id, ProjectID: project, AssignedAgentID: agent, IncarnationID: incarnation, Title: title, Body: body, Priority: input.PriorityDefault}, at); enqueueErr != nil {
				return api.DeliveryResult{}, enqueueErr
			}
		}
		result.TaskIDs = append(result.TaskIDs, id.String())
	}
	return result, nil
}

func deliveryTaskID(parts ...string) kernel.TaskID {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	id, _ := kernel.TaskIDFromBytes(sum[:16])
	return id
}

func deliveryIncarnationID(parts ...string) kernel.IncarnationID {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	id, _ := kernel.IncarnationIDFromBytes(sum[:16])
	return id
}
