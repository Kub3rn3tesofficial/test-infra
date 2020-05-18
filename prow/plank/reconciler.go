/*
Copyright 2020 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package plank

import (
	"context"
	"fmt"
	"time"

	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/clock"
	"sigs.k8s.io/controller-runtime"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	prowv1 "k8s.io/test-infra/prow/apis/prowjobs/v1"
	"k8s.io/test-infra/prow/config"
	"k8s.io/test-infra/prow/kube"
	"k8s.io/test-infra/prow/pjutil"
	"k8s.io/test-infra/prow/pod-utils/decorate"
)

const controllerName = "plank"

func Add(
	mgr controllerruntime.Manager,
	buildMgrs map[string]controllerruntime.Manager,
	cfg config.Getter,
	totURL string,
	additionalSelector string,
) error {
	return add(mgr, buildMgrs, cfg, totURL, additionalSelector, nil, nil, 10)
}

func add(
	mgr controllerruntime.Manager,
	buildMgrs map[string]controllerruntime.Manager,
	cfg config.Getter,
	totURL string,
	additionalSelector string,
	overwriteReconcile func(reconcile.Request) (reconcile.Result, error),
	predicateCallack func(bool),
	numWorkers int,
) error {
	predicate, err := predicates(additionalSelector, predicateCallack)
	if err != nil {
		return fmt.Errorf("failed to construct predicate: %w", err)
	}

	if err := mgr.GetFieldIndexer().IndexField(&prowv1.ProwJob{}, prowJobIndexName, prowJobIndexer(cfg().ProwJobNamespace)); err != nil {
		return fmt.Errorf("failed to add indexer: %w", err)
	}

	blder := controllerruntime.NewControllerManagedBy(mgr).
		Named(controllerName).
		For(&prowv1.ProwJob{}).
		WithEventFilter(predicate).
		WithOptions(controller.Options{MaxConcurrentReconciles: numWorkers})

	r := &reconciler{
		ctx:                context.Background(),
		pjClient:           mgr.GetClient(),
		buildClients:       map[string]ctrlruntimeclient.Client{},
		overwriteReconcile: overwriteReconcile,
		log:                logrus.NewEntry(logrus.StandardLogger()),
		config:             cfg,
		totURL:             totURL,
		clock:              clock.RealClock{},
	}
	for buildCluster, buildClusterMgr := range buildMgrs {
		blder = blder.Watches(
			source.NewKindWithCache(&corev1.Pod{}, buildClusterMgr.GetCache()),
			podEventRequestMapper(cfg().ProwJobNamespace))
		r.buildClients[buildCluster] = buildClusterMgr.GetClient()
	}

	if err := blder.Complete(r); err != nil {
		return fmt.Errorf("failed to build controller: %w", err)
	}

	if err := mgr.Add(manager.RunnableFunc(r.syncMetrics)); err != nil {
		return fmt.Errorf("failed to add metrics runnable to manager: %w", err)
	}

	return nil
}

type reconciler struct {
	ctx                context.Context
	pjClient           ctrlruntimeclient.Client
	buildClients       map[string]ctrlruntimeclient.Client
	overwriteReconcile func(reconcile.Request) (reconcile.Result, error)
	log                *logrus.Entry
	config             config.Getter
	totURL             string
	clock              clock.Clock
}

func (r *reconciler) syncMetrics(stop <-chan struct{}) error {
	for {
		select {
		case <-stop:
			return nil
		case <-time.NewTicker(30 * time.Second).C:
			pjs := &prowv1.ProwJobList{}
			if err := r.pjClient.List(r.ctx, pjs, optAllProwJobs()); err != nil {
				r.log.WithError(err).Error("failed to list prowjobs for metrics")
				continue
			}
			kube.GatherProwJobMetrics(pjs.Items)
		}
	}
}

func (r *reconciler) Reconcile(request reconcile.Request) (reconcile.Result, error) {
	if r.overwriteReconcile != nil {
		return r.overwriteReconcile(request)
	}
	return r.defaultReconcile(request)
}

func (r *reconciler) defaultReconcile(request reconcile.Request) (reconcile.Result, error) {
	pj := &prowv1.ProwJob{}
	if err := r.pjClient.Get(r.ctx, request.NamespacedName, pj); err != nil {
		if !kerrors.IsNotFound(err) {
			return reconcile.Result{}, fmt.Errorf("failed to get prowjob %s: %v", request.Name, err)
		}

		// Objects can be deleted from the API while being in our workqueue
		return reconcile.Result{}, nil
	}

	// TODO: Terminal errors for unfixable cases like missing build clusters
	// and not return an error to prevent requeuing?
	res, err := r.reconcile(pj)
	if res == nil {
		res = &reconcile.Result{}
	}
	if err != nil {
		r.log.WithError(err).Error("Reconciliation failed")
	}
	return *res, err
}

func (r *reconciler) reconcile(pj *prowv1.ProwJob) (*reconcile.Result, error) {
	// terminateDupes first, as that might reduce cluster load and prevent us
	// from doing pointless work.
	if err := r.terminateDupes(pj); err != nil {
		return nil, fmt.Errorf("terminateDupes failed: %w", err)
	}

	switch pj.Status.State {
	case prowv1.PendingState:
		return nil, r.syncPendingJob(pj)
	case prowv1.TriggeredState:
		return r.syncTriggeredJob(pj)
	case prowv1.AbortedState:
		return nil, r.syncAbortedJob(pj)
	}

	return nil, nil
}

func (r *reconciler) terminateDupes(pj *prowv1.ProwJob) error {
	pjs := &prowv1.ProwJobList{}
	if err := r.pjClient.List(r.ctx, pjs, optNotCompletedProwJobs()); err != nil {
		return fmt.Errorf("failed to list prowjobs: %v", err)
	}

	return pjutil.TerminateOlderJobs(r.pjClient, r.log, pjs.Items, func(toCancel prowv1.ProwJob) error {
		client, ok := r.buildClients[pj.ClusterAlias()]
		if !ok {
			return fmt.Errorf("no client for cluster %q present", pj.ClusterAlias())
		}
		podToDelete := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: r.config().PodNamespace,
				Name:      toCancel.Name,
			},
		}
		if err := client.Delete(r.ctx, podToDelete); err != nil {
			return fmt.Errorf("failed to delete pod %s/%s: %w", podToDelete.Namespace, podToDelete.Name, err)
		}
		return nil
	})
}

func (r *reconciler) syncPendingJob(pj *prowv1.ProwJob) error {
	prevPJ := pj.DeepCopy()

	pod, podExists, err := r.pod(pj)
	if err != nil {
		return err
	}

	if !podExists {
		// Pod is missing. This can happen in case the previous pod was deleted manually or by
		// a rescheduler. Start a new pod.
		id, pn, err := r.startPod(pj)
		if err != nil {
			if !isRequestError(err) {
				return fmt.Errorf("error starting pod %s: %v", pod.Name, err)
			}
			pj.Status.State = prowv1.ErrorState
			pj.SetComplete()
			pj.Status.Description = "Job cannot be processed."
			r.log.WithFields(pjutil.ProwJobFields(pj)).WithError(err).Warning("Unprocessable pod.")
		} else {
			pj.Status.BuildID = id
			pj.Status.PodName = pn
			r.log.WithFields(pjutil.ProwJobFields(pj)).Info("Pod is missing, starting a new pod")
		}
	} else {

		switch pod.Status.Phase {
		case corev1.PodUnknown:
			// Pod is in Unknown state. This can happen if there is a problem with
			// the node. Delete the old pod, we'll start a new one next loop.
			r.log.WithFields(pjutil.ProwJobFields(pj)).Info("Pod is in unknown state, deleting & restarting pod")
			client, ok := r.buildClients[pj.ClusterAlias()]
			if !ok {
				return fmt.Errorf("unknown pod %s: unknown cluster alias %q", pod.Name, pj.ClusterAlias())
			}

			r.log.WithField("name", pj.ObjectMeta.Name).Debug("Delete Pod.")
			return client.Delete(r.ctx, pod)

		case corev1.PodSucceeded:
			// Pod succeeded. Update ProwJob, talk to GitHub, and start next jobs.
			pj.SetComplete()
			pj.Status.State = prowv1.SuccessState
			pj.Status.Description = "Job succeeded."

		case corev1.PodFailed:
			if pod.Status.Reason == Evicted {
				// Pod was evicted.
				if pj.Spec.ErrorOnEviction {
					// ErrorOnEviction is enabled, complete the PJ and mark it as errored.
					pj.SetComplete()
					pj.Status.State = prowv1.ErrorState
					pj.Status.Description = "Job pod was evicted by the cluster."
					break
				}
				// ErrorOnEviction is disabled. Delete the pod now and recreate it in
				// the next resync.
				client, ok := r.buildClients[pj.ClusterAlias()]
				if !ok {
					return fmt.Errorf("evicted pod %s: unknown cluster alias %q", pod.Name, pj.ClusterAlias())
				}
				r.log.WithField("name", pj.ObjectMeta.Name).Debug("Delete Pod.")
				return client.Delete(r.ctx, pod)
			}
			// Pod failed. Update ProwJob, talk to GitHub.
			pj.SetComplete()
			pj.Status.State = prowv1.FailureState
			pj.Status.Description = "Job failed."

		case corev1.PodPending:
			maxPodPending := r.config().Plank.PodPendingTimeout.Duration
			maxPodUnscheduled := r.config().Plank.PodUnscheduledTimeout.Duration
			if pod.Status.StartTime.IsZero() {
				if time.Since(pod.CreationTimestamp.Time) >= maxPodUnscheduled {
					// Pod is stuck in unscheduled state longer than maxPodUncheduled
					// abort the job, and talk to GitHub
					pj.SetComplete()
					pj.Status.State = prowv1.ErrorState
					pj.Status.Description = "Pod scheduling timeout."
					r.log.WithFields(pjutil.ProwJobFields(pj)).Info("Marked job for stale unscheduled pod as errored.")
					break
				}
			} else if time.Since(pod.Status.StartTime.Time) >= maxPodPending {
				// Pod is stuck in pending state longer than maxPodPending
				// abort the job, and talk to GitHub
				pj.SetComplete()
				pj.Status.State = prowv1.ErrorState
				pj.Status.Description = "Pod pending timeout."
				r.log.WithFields(pjutil.ProwJobFields(pj)).Info("Marked job for stale pending pod as errored.")
				break
			}
			// Pod is running. Do nothing.
			return nil
		case corev1.PodRunning:
			maxPodRunning := r.config().Plank.PodRunningTimeout.Duration
			if pod.Status.StartTime.IsZero() || time.Since(pod.Status.StartTime.Time) < maxPodRunning {
				// Pod is still running. Do nothing.
				return nil
			}

			// Pod is stuck in running state longer than maxPodRunning
			// abort the job, and talk to GitHub
			pj.SetComplete()
			pj.Status.State = prowv1.AbortedState
			pj.Status.Description = "Pod running timeout."
			client, ok := r.buildClients[pj.ClusterAlias()]
			if !ok {
				return fmt.Errorf("running pod %s: unknown cluster alias %q", pod.Name, pj.ClusterAlias())
			}
			if err := client.Delete(r.ctx, pod); err != nil {
				return fmt.Errorf("failed to delete pod %s that was in running timeout: %v", pod.Name, err)
			}
			r.log.WithFields(pjutil.ProwJobFields(pj)).Info("Deleted stale running pod.")
		default:
			// other states, ignore
			return nil
		}
	}

	pj.Status.URL, err = pjutil.JobURL(r.config().Plank, *pj, r.log)
	if err != nil {
		r.log.WithFields(pjutil.ProwJobFields(pj)).WithError(err).Warn("failed to get jobURL")
	}

	if prevPJ.Status.State != pj.Status.State {
		r.log.WithFields(pjutil.ProwJobFields(pj)).
			WithField("from", prevPJ.Status.State).
			WithField("to", pj.Status.State).Info("Transitioning states.")
	}

	if err := r.pjClient.Patch(r.ctx, pj.DeepCopy(), ctrlruntimeclient.MergeFrom(prevPJ)); err != nil {
		return fmt.Errorf("patching prowjob: %v", err)
	}

	return nil
}

func (r *reconciler) syncTriggeredJob(pj *prowv1.ProwJob) (*reconcile.Result, error) {
	prevPJ := pj.DeepCopy()

	var id, pn string

	pod, podExists, err := r.pod(pj)
	if err != nil {
		return nil, err
	}
	// We may end up in a state where the pod exists but the prowjob is not
	// updated to pending if we successfully create a new pod in a previous
	// sync but the prowjob update fails. Simply ignore creating a new pod
	// and rerun the prowjob update.
	if !podExists {
		// Do not start more jobs than specified and check again later.
		canExecuteConcurrently, err := r.canExecuteConcurrently(pj)
		if err != nil {
			return nil, fmt.Errorf("canExecuteConcurrently: %v", err)
		}
		if !canExecuteConcurrently {
			return &reconcile.Result{RequeueAfter: 10 * time.Second}, nil
		}
		// We haven't started the pod yet. Do so.
		id, pn, err = r.startPod(pj)
		if err != nil {
			if !isRequestError(err) {
				return nil, fmt.Errorf("error starting pod: %v", err)
			}
			pj.Status.State = prowv1.ErrorState
			pj.SetComplete()
			pj.Status.Description = "Job cannot be processed."
			logrus.WithField("job", pj.Spec.Job).WithError(err).Warning("Unprocessable pod.")
		}
	} else {
		id = getPodBuildID(pod)
		pn = pod.ObjectMeta.Name
	}

	if pj.Status.State == prowv1.TriggeredState {
		// BuildID needs to be set before we execute the job url template.
		pj.Status.BuildID = id
		now := metav1.NewTime(r.clock.Now())
		pj.Status.PendingTime = &now
		pj.Status.State = prowv1.PendingState
		pj.Status.PodName = pn
		pj.Status.Description = "Job triggered."
		pj.Status.URL, err = pjutil.JobURL(r.config().Plank, *pj, r.log)
		if err != nil {
			r.log.WithFields(pjutil.ProwJobFields(pj)).WithError(err).Warn("failed to get jobURL")
		}
	}

	if prevPJ.Status.State != pj.Status.State {
		r.log.WithFields(pjutil.ProwJobFields(pj)).
			WithField("from", prevPJ.Status.State).
			WithField("to", pj.Status.State).Info("Transitioning states.")
	}
	return nil, r.pjClient.Patch(r.ctx, pj.DeepCopy(), ctrlruntimeclient.MergeFrom(prevPJ))
}

func (r *reconciler) syncAbortedJob(pj *prowv1.ProwJob) error {

	buildClient, ok := r.buildClients[pj.ClusterAlias()]
	if !ok {
		return fmt.Errorf("no build client available for cluster %s", pj.ClusterAlias())
	}

	// Just optimistically delete and swallow the potential 404
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Name:      pj.Name,
		Namespace: r.config().PodNamespace,
	}}
	if err := buildClient.Delete(r.ctx, pod); err != nil && !kerrors.IsNotFound(err) {
		return fmt.Errorf("failed to delete pod %s/%s in cluster %s: %w", pod.Namespace, pod.Name, pj.ClusterAlias(), err)
	}

	originalPJ := pj.DeepCopy()
	pj.SetComplete()
	return r.pjClient.Patch(r.ctx, pj, ctrlruntimeclient.MergeFrom(originalPJ))
}

func (r *reconciler) pod(pj *prowv1.ProwJob) (*corev1.Pod, bool, error) {
	buildClient, buildClientExists := r.buildClients[pj.ClusterAlias()]
	if !buildClientExists {
		// TODO: Use terminal error type to prevent requeuing, this wont be fixed without
		// a restart
		return nil, false, fmt.Errorf("no build client found for cluster %q", pj.ClusterAlias())
	}

	pod := &corev1.Pod{}
	name := types.NamespacedName{
		Namespace: r.config().PodNamespace,
		Name:      pj.Name,
	}

	if err := buildClient.Get(r.ctx, name, pod); err != nil {
		if kerrors.IsNotFound(err) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("failed to get pod: %v", err)
	}

	return pod, true, nil
}

func (r *reconciler) startPod(pj *prowv1.ProwJob) (string, string, error) {
	buildID, err := r.getBuildID(pj.Spec.Job)
	if err != nil {
		return "", "", fmt.Errorf("error getting build ID: %v", err)
	}

	pod, err := decorate.ProwJobToPod(*pj, buildID)
	if err != nil {
		return "", "", err
	}
	pod.Namespace = r.config().PodNamespace

	client, ok := r.buildClients[pj.ClusterAlias()]
	if !ok {
		// TODO: Terminal error to prevent requeuing
		return "", "", fmt.Errorf("unknown cluster alias %q", pj.ClusterAlias())
	}
	err = client.Create(r.ctx, pod)
	r.log.WithFields(pjutil.ProwJobFields(pj)).Debug("Create Pod.")
	if err != nil {
		return "", "", err
	}
	return buildID, pod.Name, nil
}

func (r *reconciler) getBuildID(name string) (string, error) {
	return pjutil.GetBuildID(name, r.totURL)
}

// canExecuteConcurrently determines if the cocurrency settings allow our job
// to be started. We start jobs with a limited concurrency in order, oldest
// first. This allows us to get away without any global locking by just looking
// at the jobs in the cluster.
func (r *reconciler) canExecuteConcurrently(pj *prowv1.ProwJob) (bool, error) {

	if max := r.config().Plank.MaxConcurrency; max > 0 {
		pjs := &prowv1.ProwJobList{}
		if err := r.pjClient.List(r.ctx, pjs, optNotCompletedProwJobs()); err != nil {
			return false, fmt.Errorf("failed to list prowjobs: %w", err)
		}
		// The list contains our own ProwJob
		running := len(pjs.Items) - 1
		if running >= max {
			r.log.WithFields(pjutil.ProwJobFields(pj)).Infof("Not starting another job, already %d running.", running)
			return false, nil
		}
	}

	if pj.Spec.MaxConcurrency == 0 {
		return true, nil
	}

	pjs := &prowv1.ProwJobList{}
	if err := r.pjClient.List(r.ctx, pjs, optNotCompletedProwJobsNamed(pj.Spec.Job)); err != nil {
		return false, fmt.Errorf("failed listing prowjobs: %w:", err)
	}
	r.log.Infof("got %d not completed with same name", len(pjs.Items))

	var olderMatchingPJs int
	for _, foundPJ := range pjs.Items {
		// Ignore self here. Second half of the condition is needed for tests.
		if foundPJ.UID == pj.UID && pj.UID != types.UID("") {
			continue
		}

		if foundPJ.CreationTimestamp.Before(&pj.CreationTimestamp) {
			olderMatchingPJs++
		}

	}

	if olderMatchingPJs >= pj.Spec.MaxConcurrency {
		r.log.WithFields(pjutil.ProwJobFields(pj)).
			Debugf("Not starting another instance of %s, already %d older instances waiting and %d is the limit",
				pj.Spec.Job, olderMatchingPJs, pj.Spec.MaxConcurrency)
		return false, nil
	}

	return true, nil
}

func predicatesFromFilter(filter func(m metav1.Object, r runtime.Object) bool) predicate.Predicate {
	return predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			return filter(e.Meta, e.Object)
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			return filter(e.Meta, e.Object)
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			return filter(e.MetaNew, e.ObjectNew)
		},
		GenericFunc: func(e event.GenericEvent) bool {
			return filter(e.Meta, e.Object)
		},
	}

}

func predicates(additionalSelector string, callback func(bool)) (predicate.Predicate, error) {
	rawSelector := fmt.Sprintf("%s=true", kube.CreatedByProw)
	if additionalSelector != "" {
		rawSelector = fmt.Sprintf("%s,%s", rawSelector, additionalSelector)
	}
	selector, err := labels.Parse(rawSelector)
	if err != nil {
		return nil, fmt.Errorf("failed to parse label selector %s: %w", rawSelector, err)
	}

	return predicatesFromFilter(func(m metav1.Object, r runtime.Object) bool {
		result := func() bool {
			pj, ok := r.(*prowv1.ProwJob)
			if !ok {
				// We ignore pods that do not match our selector
				return selector.Matches(labels.Set(m.GetLabels()))
			}

			// We can ignore completed prowjobs
			if pj.Complete() {
				return false
			}

			return pj.Spec.Agent == prowv1.KubernetesAgent
		}()
		if callback != nil {
			callback(result)
		}
		return result
	}), nil
}

func podEventRequestMapper(prowJobNamespace string) handler.EventHandler {
	return &handler.EnqueueRequestsFromMapFunc{
		ToRequests: handler.ToRequestsFunc(func(mo handler.MapObject) []controllerruntime.Request {
			return []controllerruntime.Request{{NamespacedName: ctrlruntimeclient.ObjectKey{
				Namespace: prowJobNamespace,
				Name:      mo.Meta.GetName(),
			}},
			}
		}),
	}
}

const (
	// prowJobIndexName is the name of an index that
	// holds all ProwJobs that are in the correct namespace
	// and use the Kubernetes agent
	prowJobIndexName = "plank-prow-jobs"
	// prowJobIndexKeyAll is the indexKey for all ProwJobs
	prowJobIndexKeyAll = "all"
	// prowJobIndexKeyCompleted is the indexKey for not
	// completed ProwJobs
	prowJobIndexKeyNotCompleted = "not-completed"
)

func prowJobIndexKeyNotCompletedByName(jobName string) string {
	return fmt.Sprintf("not-completed-%s", jobName)
}

func prowJobIndexer(prowJobNamespace string) ctrlruntimeclient.IndexerFunc {
	return func(o runtime.Object) []string {
		pj := o.(*prowv1.ProwJob)
		if pj.Namespace != prowJobNamespace || pj.Spec.Agent != prowv1.KubernetesAgent {
			return nil
		}

		if !pj.Complete() {
			return []string{
				prowJobIndexKeyAll,
				prowJobIndexKeyNotCompleted,
				prowJobIndexKeyNotCompletedByName(pj.Spec.Job),
			}
		}

		return []string{prowJobIndexKeyAll}
	}
}

func optAllProwJobs() ctrlruntimeclient.ListOption {
	return ctrlruntimeclient.MatchingField(prowJobIndexName, prowJobIndexKeyAll)
}

func optNotCompletedProwJobs() ctrlruntimeclient.ListOption {
	return ctrlruntimeclient.MatchingField(prowJobIndexName, prowJobIndexKeyNotCompleted)
}

func optNotCompletedProwJobsNamed(name string) ctrlruntimeclient.ListOption {
	return ctrlruntimeclient.MatchingField(prowJobIndexName, prowJobIndexKeyNotCompletedByName(name))
}
