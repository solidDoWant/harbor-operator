package ownership

import (
	"context"
	"errors"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/solidDoWant/harbor-operator/api/v1alpha1"
)

// HarborKeyField is the field index on status.harborKey that ownership lookups use.
const HarborKeyField = "status.harborKey"

// SetupIndexes registers the status.harborKey field index for every kind that claims Harbor objects. The manager's
// cache must have it before ProjectOwner or RobotOwner run.
func SetupIndexes(ctx context.Context, indexer client.FieldIndexer) error {
	if err := indexer.IndexField(ctx, &v1alpha1.HarborProject{}, HarborKeyField, func(object client.Object) []string {
		return harborKeyValues(object.(*v1alpha1.HarborProject).Status.HarborKey)
	}); err != nil {
		return fmt.Errorf("indexing HarborProject %s: %w", HarborKeyField, err)
	}

	if err := indexer.IndexField(ctx, &v1alpha1.HarborProjectRobot{}, HarborKeyField, func(object client.Object) []string {
		return harborKeyValues(object.(*v1alpha1.HarborProjectRobot).Status.HarborKey)
	}); err != nil {
		return fmt.Errorf("indexing HarborProjectRobot %s: %w", HarborKeyField, err)
	}

	return nil
}

func harborKeyValues(key string) []string {
	if key == "" {
		return nil
	}

	return []string{key}
}

// AdmittedFunc reports whether a claimant's reference is still admitted by the object it points at.
type AdmittedFunc[T client.Object] func(ctx context.Context, claimant T) (bool, error)

// ProjectOwner returns the HarborProject that owns the Harbor project with the given key, or nil when no claimant
// counts. It reads only the claimants of key, through the status.harborKey index.
func ProjectOwner(ctx context.Context, reader client.Reader, key string, admitted AdmittedFunc[*v1alpha1.HarborProject]) (*v1alpha1.HarborProject, error) {
	list := &v1alpha1.HarborProjectList{}
	if err := reader.List(ctx, list, client.MatchingFields{HarborKeyField: key}); err != nil {
		return nil, fmt.Errorf("listing HarborProject claimants: %w", err)
	}

	claimants := make([]*v1alpha1.HarborProject, 0, len(list.Items))
	for index := range list.Items {
		claimants = append(claimants, &list.Items[index])
	}

	return owner(ctx, claimants, func(project *v1alpha1.HarborProject) bool { return project.Status.ProjectID != 0 }, admitted)
}

// RobotOwner returns the HarborProjectRobot that owns the Harbor robot with the given key, or nil when no claimant
// counts. It reads only the claimants of key, through the status.harborKey index.
func RobotOwner(ctx context.Context, reader client.Reader, key string, admitted AdmittedFunc[*v1alpha1.HarborProjectRobot]) (*v1alpha1.HarborProjectRobot, error) {
	list := &v1alpha1.HarborProjectRobotList{}
	if err := reader.List(ctx, list, client.MatchingFields{HarborKeyField: key}); err != nil {
		return nil, fmt.Errorf("listing HarborProjectRobot claimants: %w", err)
	}

	claimants := make([]*v1alpha1.HarborProjectRobot, 0, len(list.Items))
	for index := range list.Items {
		claimants = append(claimants, &list.Items[index])
	}

	return owner(ctx, claimants, func(robot *v1alpha1.HarborProjectRobot) bool { return robot.Status.RobotID != 0 }, admitted)
}

func owner[T client.Object](ctx context.Context, objects []T, hasID func(T) bool, admitted AdmittedFunc[T]) (T, error) {
	var none T

	claimants := make([]Claimant, 0, len(objects))

	byUID := make(map[string]T, len(objects))
	for _, object := range objects {
		isAdmitted, err := admitted(ctx, object)
		if err != nil {
			return none, fmt.Errorf("checking admission of %s/%s: %w", object.GetNamespace(), object.GetName(), err)
		}

		claimants = append(claimants, Claimant{
			UID:               object.GetUID(),
			CreationTimestamp: object.GetCreationTimestamp(),
			Deleting:          !object.GetDeletionTimestamp().IsZero(),
			HasID:             hasID(object),
			Admitted:          isAdmitted,
		})
		byUID[string(object.GetUID())] = object
	}

	winner, err := Owner(claimants)
	if errors.Is(err, ErrNoOwner) {
		return none, nil
	}

	if err != nil {
		return none, err
	}

	return byUID[string(winner.UID)], nil
}
