package main

import (
	"context"
	"fmt"
	"log"
	"time"

	buildv1alpha1 "github.com/shipwright-io/build/pkg/apis/build/v1alpha1"
	buildclientv1alpha1 "github.com/shipwright-io/build/pkg/client/clientset/versioned"
	typedbuildclientv1alpha1 "github.com/shipwright-io/build/pkg/client/clientset/versioned/typed/build/v1alpha1"
	"github.com/shipwright-io/build/pkg/client/informers/externalversions"
	buildlister "github.com/shipwright-io/build/pkg/client/listers/build/v1alpha1"
	"github.com/tektoncd/pipeline/pkg/apis/pipeline/v1alpha1"
	runinformer "github.com/tektoncd/pipeline/pkg/client/injection/informers/pipeline/v1alpha1/run"
	runreconciler "github.com/tektoncd/pipeline/pkg/client/injection/reconciler/pipeline/v1alpha1/run"
	tknlister "github.com/tektoncd/pipeline/pkg/client/listers/pipeline/v1alpha1"
	tkncontroller "github.com/tektoncd/pipeline/pkg/controller"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"knative.dev/pkg/apis"
	"knative.dev/pkg/configmap"
	"knative.dev/pkg/controller"
	"knative.dev/pkg/injection/sharedmain"
	"knative.dev/pkg/logging"
	"knative.dev/pkg/reconciler"
)

const (
	controllerName = "build-task-controller"
	resyncPeriod   = 10 * time.Hour
)

func main() {
	sharedmain.Main(controllerName, newController)
}

func newController(ctx context.Context, cmw configmap.Watcher) *controller.Impl {
	c := &Reconciler{}
	impl := runreconciler.NewImpl(ctx, c, func(impl *controller.Impl) controller.Options {
		return controller.Options{
			AgentName: controllerName,
		}
	})

	// Watch all Runs<Build>
	runinformer.Get(ctx).Informer().AddEventHandler(cache.FilteringResourceEventHandler{
		FilterFunc: tkncontroller.FilterRunRef("shipwright.io/v1alpha1", "Build"),
		Handler:    controller.HandleAll(impl.Enqueue),
	})
	c.enqueueRun = impl.Enqueue
	c.runLister = runinformer.Get(ctx).Lister()

	// Watch all BuildRuns owned by Runs
	r, err := rest.InClusterConfig()
	if err != nil {
		log.Fatal(err)
	}
	c.buildClient = typedbuildclientv1alpha1.NewForConfigOrDie(r)
	// TODO: only hear about BuildRuns owned by Runs.
	sif := externalversions.NewSharedInformerFactoryWithOptions(buildclientv1alpha1.NewForConfigOrDie(r), resyncPeriod)
	sif.Shipwright().V1alpha1().BuildRuns().Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.reconcileBuildRun(ctx, obj.(*buildv1alpha1.BuildRun)) },
		UpdateFunc: func(_, obj interface{}) { c.reconcileBuildRun(ctx, obj.(*buildv1alpha1.BuildRun)) },
		DeleteFunc: func(obj interface{}) { c.reconcileBuildRun(ctx, obj.(*buildv1alpha1.BuildRun)) },
	})
	stopCh := make(chan struct{})
	sif.WaitForCacheSync(stopCh)
	go sif.Start(stopCh)
	c.buildRunLister = sif.Shipwright().V1alpha1().BuildRuns().Lister()

	return impl
}

type Reconciler struct {
	buildClient    *typedbuildclientv1alpha1.ShipwrightV1alpha1Client // For creating BuildRuns
	buildRunLister buildlister.BuildRunLister                         // For getting BuildRuns
	runLister      tknlister.RunLister                                // For getting Runs
	enqueueRun     func(r interface{})                                // For enqueueing Run reconciliations
}

type extraFields struct {
	BuildRunName string `json:"buildRunName,omitempty"`
}

func (c *Reconciler) reconcileBuildRun(ctx context.Context, br *buildv1alpha1.BuildRun) {
	logger := logging.FromContext(ctx)
	logger.Infof("Reconciling BuildRun %q", br.Name)

	// For every BuildRun update, look up the Run that owns it (if any;
	// ignore otherwise), and enqueue a reconcile the Run in a workqueue.
	for _, or := range br.OwnerReferences {
		if or.APIVersion == "tekton.dev/v1alpha1" && or.Kind == "Run" {
			logger.Infof("BuildRun %s is owned by Run %s", br.Name, or.Name)
			r, err := c.runLister.Runs(br.Namespace).Get(or.Name)
			if err != nil {
				logger.Errorf("Failed to get Run %q: %v", or.Name, err)
				return
			}
			logger.Infof("Found Run that owns %s (%s), enqueueing reconcile", br.Name, r.Name)
			c.enqueueRun(r)
			return
		}
	}
	logger.Infof("BuildRun %q had no owning Run", br.Name)
}

// ReconcileKind implements Interface.ReconcileKind.
func (c *Reconciler) ReconcileKind(ctx context.Context, r *v1alpha1.Run) reconciler.Event {
	logger := logging.FromContext(ctx)
	logger.Infof("Reconciling %s/%s", r.Namespace, r.Name)

	if r.IsDone() {
		logger.Info("Run is finished, done reconciling")
		return nil
	}

	var ex extraFields
	if err := r.Status.DecodeExtraFields(&ex); err != nil {
		return err
	}
	var br *buildv1alpha1.BuildRun
	var err error
	if ex.BuildRunName != "" {
		// Get the BuildRun
		logger.Infof("BuildRun associated with %q is %q", r.Name, ex.BuildRunName)
		// br, err = c.buildRunLister.BuildRuns(r.Namespace).Get(ex.BuildRunName)
		br, err = c.buildClient.BuildRuns(r.Namespace).Get(ctx, ex.BuildRunName, metav1.GetOptions{}) // TODO slow, remove
		if err != nil {
			return err
		}
	} else {
		logger.Infof("Run %q has no BuildRun associated, creating one", r.Name)
		// Create the BuildRun, owned by the Run.
		// TODO: r.Params -> br.Params
		br, err = c.buildClient.BuildRuns(r.Namespace).Create(ctx, &buildv1alpha1.BuildRun{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: fmt.Sprintf("%s-buildrun-", r.Name),
				OwnerReferences: []metav1.OwnerReference{{
					APIVersion: "tekton.dev/v1alpha1",
					Kind:       "Run",
					Name:       r.Name,
					UID:        r.UID,
				}},
			},
			Spec: buildv1alpha1.BuildRunSpec{
				BuildRef: &buildv1alpha1.BuildRef{
					APIVersion: r.Spec.Ref.APIVersion,
					Name:       r.Spec.Ref.Name,
				},
			},
		}, metav1.CreateOptions{})
		if err != nil {
			return err
		}
		logger.Infof("Created BuildRun %q", br.Name)

		// Set BuildRunName
		if err := r.Status.EncodeExtraFields(&extraFields{
			BuildRunName: br.Name,
		}); err != nil {
			return err
		}
		now := metav1.Now()
		r.Status.StartTime = &now
	}

	// Update the Run status from BuildRun status
	r.Status.CompletionTime = br.Status.CompletionTime
	r.Status.Conditions = nil
	for _, brc := range br.Status.Conditions {
		r.Status.Conditions = append(r.Status.Conditions, apis.Condition{
			Type:               apis.ConditionType(string(brc.Type)),
			Status:             brc.Status,
			LastTransitionTime: apis.VolatileTime{Inner: brc.LastTransitionTime},
			Reason:             brc.Reason,
			Message:            brc.Message,
			// BuildRun Conditions have no Severity.
		})
	}
	if len(r.Status.Conditions) == 0 {
		r.Status.Conditions = []apis.Condition{{
			Type:               apis.ConditionSucceeded,
			Status:             corev1.ConditionUnknown,
			LastTransitionTime: apis.VolatileTime{Inner: metav1.Now()},
		}}
	}
	// TODO: r.Status.Results["image-digest"] = built image digest.

	return reconciler.NewEvent(corev1.EventTypeNormal, "RunReconciled", "Run reconciled: \"%s/%s\"", r.Namespace, r.Name)
}
