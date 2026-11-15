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

	if err := indexer.IndexField(ctx, &v1alpha1.HarborRetentionPolicy{}, HarborKeyField, func(object client.Object) []string {
		return harborKeyValues(object.(*v1alpha1.HarborRetentionPolicy).Status.HarborKey)
	}); err != nil {
		return fmt.Errorf("indexing HarborRetentionPolicy %s: %w", HarborKeyField, err)
	}

	if err := indexer.IndexField(ctx, &v1alpha1.HarborSystemRobot{}, HarborKeyField, func(object client.Object) []string {
		return harborKeyValues(object.(*v1alpha1.HarborSystemRobot).Status.HarborKey)
	}); err != nil {
		return fmt.Errorf("indexing HarborSystemRobot %s: %w", HarborKeyField, err)
	}

	if err := indexer.IndexField(ctx, &v1alpha1.HarborImmutabilityPolicy{}, HarborKeyField, func(object client.Object) []string {
		return harborKeyValues(object.(*v1alpha1.HarborImmutabilityPolicy).Status.HarborKey)
	}); err != nil {
		return fmt.Errorf("indexing HarborImmutabilityPolicy %s: %w", HarborKeyField, err)
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

// RobotClaimant is a robot kind that claims Harbor robots: a project robot or a system robot.
type RobotClaimant interface {
	*v1alpha1.HarborProjectRobot | *v1alpha1.HarborSystemRobot
	client.Object
}

// RobotOwner returns the robot resource of kind T that owns the Harbor robot with the given key, or nil when no
// claimant counts. It reads only the claimants of key, through the status.harborKey index.
func RobotOwner[T RobotClaimant](ctx context.Context, reader client.Reader, key string, admitted AdmittedFunc[T]) (T, error) {
	var (
		none      T
		claimants []T
	)

	switch any(none).(type) {
	case *v1alpha1.HarborProjectRobot:
		list := &v1alpha1.HarborProjectRobotList{}
		if err := reader.List(ctx, list, client.MatchingFields{HarborKeyField: key}); err != nil {
			return none, fmt.Errorf("listing HarborProjectRobot claimants: %w", err)
		}

		for index := range list.Items {
			claimants = append(claimants, any(&list.Items[index]).(T))
		}
	case *v1alpha1.HarborSystemRobot:
		list := &v1alpha1.HarborSystemRobotList{}
		if err := reader.List(ctx, list, client.MatchingFields{HarborKeyField: key}); err != nil {
			return none, fmt.Errorf("listing HarborSystemRobot claimants: %w", err)
		}

		for index := range list.Items {
			claimants = append(claimants, any(&list.Items[index]).(T))
		}
	}

	return owner(ctx, claimants, func(robot T) bool { return robotStatus(robot).RobotID != 0 }, admitted)
}

// robotStatus returns the status both robot kinds share.
func robotStatus[T RobotClaimant](robot T) *v1alpha1.HarborProjectRobotStatus {
	switch typed := any(robot).(type) {
	case *v1alpha1.HarborProjectRobot:
		return &typed.Status
	case *v1alpha1.HarborSystemRobot:
		return &typed.Status
	}

	return &v1alpha1.HarborProjectRobotStatus{}
}

// RetentionOwner returns the HarborRetentionPolicy that owns the retention policy with the given key, or nil when no
// claimant counts. It reads only the claimants of key, through the status.harborKey index.
func RetentionOwner(ctx context.Context, reader client.Reader, key string, admitted AdmittedFunc[*v1alpha1.HarborRetentionPolicy]) (*v1alpha1.HarborRetentionPolicy, error) {
	list := &v1alpha1.HarborRetentionPolicyList{}
	if err := reader.List(ctx, list, client.MatchingFields{HarborKeyField: key}); err != nil {
		return nil, fmt.Errorf("listing HarborRetentionPolicy claimants: %w", err)
	}

	claimants := make([]*v1alpha1.HarborRetentionPolicy, 0, len(list.Items))
	for index := range list.Items {
		claimants = append(claimants, &list.Items[index])
	}

	return owner(ctx, claimants, func(policy *v1alpha1.HarborRetentionPolicy) bool { return policy.Status.RetentionID != 0 }, admitted)
}

// ImmutabilityOwner returns the HarborImmutabilityPolicy that owns the immutability rule set with the given key, or
// nil when no claimant counts. The claimed ID is status.projectID.
func ImmutabilityOwner(ctx context.Context, reader client.Reader, key string, admitted AdmittedFunc[*v1alpha1.HarborImmutabilityPolicy]) (*v1alpha1.HarborImmutabilityPolicy, error) {
	list := &v1alpha1.HarborImmutabilityPolicyList{}
	if err := reader.List(ctx, list, client.MatchingFields{HarborKeyField: key}); err != nil {
		return nil, fmt.Errorf("listing HarborImmutabilityPolicy claimants: %w", err)
	}

	claimants := make([]*v1alpha1.HarborImmutabilityPolicy, 0, len(list.Items))
	for index := range list.Items {
		claimants = append(claimants, &list.Items[index])
	}

	return owner(ctx, claimants, func(policy *v1alpha1.HarborImmutabilityPolicy) bool { return policy.Status.ProjectID != 0 }, admitted)
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
