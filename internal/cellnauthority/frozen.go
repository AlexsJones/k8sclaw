package cellnauthority

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"k8s.io/apimachinery/pkg/types"
)

// FrozenSelection binds planning to an existing AgentRun. Persistence must
// precede any execution side effect. This is not a grant, receipt or readiness
// assertion; the caller must still verify artifacts and obtain host authority.
type FrozenSelection struct {
	APIVersion string            `json:"apiVersion"`
	Run        Subject           `json:"run"`
	Snapshot   SelectionSnapshot `json:"snapshot"`
	Prepared   PreparedSelection `json:"prepared"`
}

func (l Loader) FreezeRun(ctx context.Context, runKey types.NamespacedName, selection []Selection, imageBytes int64) (*FrozenSelection, error) {
	run, id, err := l.readRun(ctx, runKey)
	if err != nil {
		return nil, err
	}
	snapshot, err := l.Resolve(ctx, types.NamespacedName{Namespace: run.Namespace, Name: run.Spec.AgentRef}, selection)
	if err != nil {
		return nil, err
	}
	prepared, err := Prepare(*snapshot, imageBytes)
	if err != nil {
		return nil, err
	}
	_, current, err := l.readRun(ctx, runKey)
	if err != nil {
		return nil, err
	}
	if current != id {
		return nil, fmt.Errorf("run changed during resolution")
	}
	frozen := &FrozenSelection{APIVersion: "sympozium.ai/celln-frozen-selection-v1", Run: id, Snapshot: *snapshot, Prepared: *prepared}
	bytes, err := json.Marshal(frozen)
	if err != nil {
		return nil, err
	}
	if len(bytes) > 262144 {
		return nil, fmt.Errorf("frozen selection exceeds 256 KiB")
	}
	return frozen, nil
}

// Revalidate never returns a replacement plan or execution ID. Any observed
// subject, source or authority change refuses; the caller must reconcile an
// ambiguous existing execution, not silently start a newly authorized attempt.
func (l Loader) Revalidate(ctx context.Context, frozen FrozenSelection) error {
	if frozen.APIVersion != "sympozium.ai/celln-frozen-selection-v1" || frozen.Run.Kind != "AgentRun" || len(frozen.Snapshot.Tools) > 16 {
		return fmt.Errorf("invalid frozen selection")
	}
	selections := make([]Selection, 0, len(frozen.Snapshot.Tools))
	for _, tool := range frozen.Snapshot.Tools {
		selections = append(selections, Selection{Name: tool.Identity.Name, Revision: tool.Identity.Revision, Limits: tool.Limits.DeepCopy()})
	}
	current, err := l.FreezeRun(ctx, types.NamespacedName{Namespace: frozen.Run.Namespace, Name: frozen.Run.Name}, selections, frozen.Prepared.Composition.ImageBytes)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(*current, frozen) {
		return fmt.Errorf("frozen selection or current approval changed")
	}
	return nil
}

func (l Loader) readRun(ctx context.Context, key types.NamespacedName) (*api.AgentRun, Subject, error) {
	if l.Reader == nil || key.Namespace == "" || key.Name == "" {
		return nil, Subject{}, fmt.Errorf("reader and run identity required")
	}
	var run api.AgentRun
	if err := l.Reader.Get(ctx, key, &run); err != nil {
		return nil, Subject{}, err
	}
	if run.Spec.Backend != "celln" || run.Spec.AgentRef == "" || !run.Spec.Task.IsString() {
		return nil, Subject{}, fmt.Errorf("catalogue planning requires a Celln string-task AgentRun")
	}
	id, err := IdentifySubject("AgentRun", run.ObjectMeta, run.Spec)
	if err != nil {
		return nil, Subject{}, err
	}
	return &run, id, nil
}
