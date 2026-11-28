package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ownerBuiltinWrite is the closed set of built-in writes owner_action_prepare
// can approve. Reads, runtime prompts and capture keep their existing paths.
func ownerBuiltinWrite(tool string) bool {
	switch tool {
	case "persona_settings", "model_profile_switch", "model_profile_rollback", "model_profile_deactivate", "model_profile_clear":
		return true
	default:
		return false
	}
}

type ownerBuiltinPreparation struct {
	display           string
	revision          *int64
	modelProfileWrite bool
	profileAfter      map[string]ModelProfile
	clearsProfiles    bool
}

type ownerProfilePlan struct {
	loaded   bool
	profiles modelProfiles
}

type ownerProfilePlanContextKey struct{}

func withOwnerProfilePlan(ctx context.Context) context.Context {
	return context.WithValue(ctx, ownerProfilePlanContextKey{}, &ownerProfilePlan{})
}

func ownerProfilePlanFrom(ctx context.Context) *ownerProfilePlan {
	plan, _ := ctx.Value(ownerProfilePlanContextKey{}).(*ownerProfilePlan)
	return plan
}

// advanceOwnerProfilePlan applies one prepared catalog write only to the
// batch's private planning snapshot. The database remains untouched until each
// approved direct call passes its exact revision and display checks.
func advanceOwnerProfilePlan(ctx context.Context, prepared ownerBuiltinPreparation) error {
	if !prepared.modelProfileWrite && !prepared.clearsProfiles {
		return nil
	}
	plan := ownerProfilePlanFrom(ctx)
	if plan == nil || !plan.loaded || prepared.revision == nil || (!prepared.clearsProfiles && len(prepared.profileAfter) == 0) {
		return errors.New("a model-profile batch write did not produce its complete planned after-image")
	}
	if prepared.clearsProfiles {
		plan.profiles = modelProfiles{Revision: *prepared.revision + 1}
		return nil
	}
	for id, after := range prepared.profileAfter {
		i := -1
		for j := range plan.profiles.Profiles {
			if plan.profiles.Profiles[j].ID == id {
				i = j
				break
			}
		}
		if i < 0 {
			return fmt.Errorf("planned model profile %s disappeared", id)
		}
		plan.profiles.Profiles[i] = cloneModelProfile(after)
	}
	plan.profiles.Revision = *prepared.revision + 1
	return nil
}

// prepareOwnerBuiltin reuses the managed setter or profile dry-run. It performs
// no write and publishes no ordinary card of its own.
func (s *Service) prepareOwnerBuiltin(ctx context.Context, machine, session, tool string, input json.RawMessage) (ownerBuiltinPreparation, error) {
	if tool == "persona_settings" {
		var a personaSettingsInput
		dec := json.NewDecoder(bytes.NewReader(input))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&a); err != nil {
			return ownerBuiltinPreparation{}, err
		}
		if a.Action == "remove" && len(a.Value) == 0 && a.ID != "" {
			a.Value, _ = json.Marshal(a.ID)
		}
		if a.Action != "set" && a.Action != "add" && a.Action != "remove" {
			return ownerBuiltinPreparation{}, fmt.Errorf("persona_settings %q is read-only or not a batchable write", a.Action)
		}
		call := ToolCall{MachineID: machine, SessionID: session}
		if err := s.checkPersonaSettingLocation(ctx, call, a); err != nil {
			return ownerBuiltinPreparation{}, err
		}
		setting, found, err := s.lookupManagedOwnSetting(ctx, a.Key)
		if err != nil {
			return ownerBuiltinPreparation{}, err
		}
		if !found {
			return ownerBuiltinPreparation{}, fmt.Errorf("%s is not a managed built-in setting; use its ordinary approval path", a.Key)
		}
		change, err := s.prepareOwnSettingAction(ctx, setting, a.Action, a.Value)
		if err != nil {
			return ownerBuiltinPreparation{}, err
		}
		if change == nil || change.apply == nil {
			return ownerBuiltinPreparation{}, errors.New("the built-in setting has nothing to change; no batch item was created")
		}
		return ownerBuiltinPreparation{display: change.card(s.lang(ctx)), revision: change.revision,
			modelProfileWrite: change.modelProfileWrite, profileAfter: change.profileAfter}, nil
	}

	var approval modelProfileApproval
	var err error
	switch tool {
	case "model_profile_switch":
		var in struct {
			ID string `json:"id"`
		}
		dec := json.NewDecoder(bytes.NewReader(input))
		dec.DisallowUnknownFields()
		if err = dec.Decode(&in); err == nil {
			approval, err = s.prepareModelProfileSwitch(ctx, in.ID)
		}
	case "model_profile_rollback":
		var in struct{}
		dec := json.NewDecoder(bytes.NewReader(input))
		dec.DisallowUnknownFields()
		if err = dec.Decode(&in); err == nil {
			approval, err = s.prepareModelProfileRollback(ctx)
		}
	case "model_profile_deactivate", "model_profile_clear":
		if err = decodeManagedProfileJSON(input, &struct{}{}); err == nil {
			approval, err = s.prepareModelProfileRemoval(ctx, tool)
		}
	default:
		return ownerBuiltinPreparation{}, fmt.Errorf("%s is not a supported built-in write", tool)
	}
	if err != nil {
		return ownerBuiltinPreparation{}, err
	}
	return ownerBuiltinPreparation{display: approval.display, revision: &approval.revision, clearsProfiles: tool == "model_profile_clear"}, nil
}

func ownerChangeMatches(change *ownChange, display string, item ownerActionItem) bool {
	if item.Display == "" || display != item.Display {
		return false
	}
	return item.Revision == nil || change.revision != nil && *change.revision == *item.Revision
}

func (s *Service) consumeOwnerBuiltin(ctx context.Context, machine, session, tool string, input json.RawMessage) (*ownerActionExecution, error) {
	if !ownerBuiltinWrite(tool) {
		return nil, nil
	}
	book, err := s.ownerActions()
	if err != nil {
		// A store without the exact-approval CAS capability cannot contain a
		// built-in grant. Keep the ordinary direct-call path available.
		return nil, nil
	}
	return book.consumeBuiltin(ctx, machine, session, tool, input)
}

type ownerActionCompletedError struct{ err error }

func (e ownerActionCompletedError) Error() string { return e.err.Error() }
func (e ownerActionCompletedError) Unwrap() error { return e.err }

// finishOwnerBuiltin turns a failed built-in call into an explicit partial
// result and makes later approved items unusable. Success is item-scoped; it
// never claims that a whole batch completed.
func (s *Service) finishOwnerBuiltin(ctx context.Context, execution ownerActionExecution, text string, runErr error) (string, error) {
	book, err := s.ownerActions()
	if err != nil {
		return "", err
	}
	completed := false
	var afterWrite ownerActionCompletedError
	if errors.As(runErr, &afterWrite) {
		completed = true
		runErr = afterWrite.err
	}
	report, recordErr := book.finishBuiltin(ctx, execution, runErr, completed)
	if runErr != nil {
		if recordErr != nil {
			return "", fmt.Errorf("built-in batch item %d/%d failed: %v; recording the stopping point also failed: %v. Do not retry or execute later items", execution.Position, execution.Total, runErr, recordErr)
		}
		return "", errors.New(report)
	}
	if recordErr != nil {
		return "", fmt.Errorf("built-in batch item %d/%d completed, but recording its outcome failed: %v. Do not retry it or execute later items", execution.Position, execution.Total, recordErr)
	}
	suffix := fmt.Sprintf("Owner-action batch item %d/%d completed (%s).", execution.Position, execution.Total, execution.Item.ID)
	if strings.TrimSpace(text) == "" {
		return suffix, nil
	}
	return text + "\n" + suffix, nil
}
