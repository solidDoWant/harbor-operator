package controller

import (
	"context"
	"fmt"
	"regexp"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	harborv1alpha1 "github.com/solidDoWant/harbor-operator/api/v1alpha1"
	"github.com/solidDoWant/harbor-operator/internal/harbor"
)

// contentsRequeue is the delay before the next page of repositories is deleted. It only needs to be non-zero: the run
// carries on at once, one page per reconcile.
const contentsRequeue = time.Millisecond

// contentsLeftPattern finds the progress text in an earlier condition message, so a failed listing keeps the last
// count.
var contentsLeftPattern = regexp.MustCompile(`deleting contents: \d+ repositories left`)

func contentsLeftMessage(left int64) string {
	return fmt.Sprintf("deleting contents: %d repositories left", left)
}

// deleteContents runs when Harbor refused to delete a project that still holds repositories and the project's policy is
// DeleteWithContents. When the connection allows it, it deletes the first page of repositories and requeues, so the
// next reconcile checks the project's ID again and tries the project delete again. blocked is Harbor's refusal.
func (r *HarborProjectReconciler) deleteContents(ctx context.Context, project *harborv1alpha1.HarborProject, connection *harborv1alpha1.HarborConnection, harborClient *harbor.Client, blocked error) (reconcile.Result, error) {
	if !connection.Spec.AllowContentDeletion {
		return r.contentDeletionNotAllowed(ctx, project, connection, blocked)
	}

	name := projectName(project)

	page, err := harborClient.ListRepositories(ctx, name)
	if err != nil {
		return r.contentDeletionFailed(ctx, project, err, "repository:list", lastContentsLeft(project))
	}

	// Only repositories of this resource's own project are deleted. Another project ID means the name now belongs to a
	// project this resource never created or adopted, so the resource is released as for a changed ID.
	for _, repository := range page.Repositories {
		if repository.ProjectID != int64(project.Status.ProjectID) {
			return reconcile.Result{}, r.release(ctx, project)
		}
	}

	var answeredNotFound []string

	for index, repository := range page.Repositories {
		err := harborClient.DeleteRepository(ctx, name, repository.Name)

		switch {
		case harbor.IsNotFound(err):
			answeredNotFound = append(answeredNotFound, repository.Name)
		case err != nil:
			return r.contentDeletionFailed(ctx, project, err, "repository:delete", contentsLeftMessage(page.Total-int64(index)))
		}
	}

	left, err := harborClient.ListRepositories(ctx, name)
	if err != nil {
		return r.contentDeletionFailed(ctx, project, err, "repository:list", contentsLeftMessage(page.Total-int64(len(page.Repositories))))
	}

	// A 404 counts as deleted only when the repository is gone from the listing. Otherwise the name sent to Harbor was
	// wrong, and deleting again would loop forever.
	for _, repository := range left.Repositories {
		for _, missing := range answeredNotFound {
			if repository.Name == missing {
				return r.deleteFailed(ctx, project, harborv1alpha1.ReasonInvalidSpec,
					fmt.Sprintf("repository delete answered 404 but the repository %q still exists", missing))
			}
		}
	}

	if err := r.setReady(ctx, project, metav1.ConditionFalse, harborv1alpha1.ReasonDeleteBlocked, contentsLeftMessage(left.Total)); err != nil {
		return reconcile.Result{}, err
	}

	return reconcile.Result{RequeueAfter: contentsRequeue}, nil
}

// contentDeletionNotAllowed reports that the project's policy asks to delete its contents but the connection does not
// allow it. The project then waits like one with the Delete policy until its images are removed some other way.
func (r *HarborProjectReconciler) contentDeletionNotAllowed(ctx context.Context, project *harborv1alpha1.HarborProject, connection *harborv1alpha1.HarborConnection, blocked error) (reconcile.Result, error) {
	key := client.ObjectKeyFromObject(connection)
	message := fmt.Sprintf("%s; content deletion is not allowed by HarborConnection %s", blocked.Error(), key)

	previous := meta.FindStatusCondition(project.Status.Conditions, harborv1alpha1.ConditionReady)
	if previous == nil || previous.Reason != harborv1alpha1.ReasonDeleteBlocked || previous.Message != message {
		r.Recorder.Eventf(project, nil, corev1.EventTypeWarning, harborv1alpha1.ReasonContentDeletionNotAllowed, "Delete",
			"content deletion is not allowed by HarborConnection %s; the images are left in place", key)
	}

	return r.deleteFailed(ctx, project, harborv1alpha1.ReasonDeleteBlocked, message)
}

// contentDeletionFailed keeps the finalizer and reports the failure with what is left. A 403 names the permission of
// the call that failed.
func (r *HarborProjectReconciler) contentDeletionFailed(ctx context.Context, project *harborv1alpha1.HarborProject, err error, permission, progress string) (reconcile.Result, error) {
	reason, _ := harbor.MapError(err)

	message := err.Error()
	if reason == harborv1alpha1.ReasonForbidden {
		message = fmt.Sprintf("the credential lacks %s at project scope: %s", permission, message)
	}

	if progress != "" {
		message += "; " + progress
	}

	return r.deleteFailed(ctx, project, reason, message)
}

// lastContentsLeft is the progress text of the current condition, or "" when there is none.
func lastContentsLeft(project *harborv1alpha1.HarborProject) string {
	previous := meta.FindStatusCondition(project.Status.Conditions, harborv1alpha1.ConditionReady)
	if previous == nil {
		return ""
	}

	return contentsLeftPattern.FindString(previous.Message)
}
