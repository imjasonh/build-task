# Shipwright Custom Task for Tekton

## What?

This controller watches for Custom Task `Run`s that reference a Shipwright `Build`, and responds by creating a `BuildRun`.
It then also watches `BuildRun`s owned by `Run`s, and updates the associated `Run` with `BuildRun` status information.

## Why?

Two reasons:

1. To integrate with Tekton Triggers, which is configured to only create Tekton
   resources (including `Run`s), but which cannot create `BuildRun`s directly.
1. To integrate with Shipwright image build workflows with `PipelineRun`s directly;
   this allows a Pipeline author to request a Shipwright BuildRun as part of their Pipeline.

## Demo

### Prerequisites:

1. Install Tekton
1. Install Shipwright and the `kaniko` ClusterBuildStrategy
1. Install the Custom Task controller using [`ko`](https://github.com/google/ko)

```
kubectl apply -f https://storage.googleapis.com/tekton-releases/pipeline/latest/release.yaml
kubectl apply -f https://github.com/shipwright-io/build/releases/download/v0.4.0/release.yaml
kubectl apply -f https://raw.githubusercontent.com/shipwright-io/build/v0.4.0/samples/buildstrategy/kaniko/buildstrategy_kaniko_cr.yaml
ko apply -f controller.yaml
```

### Create `Run`s Directly

First, we'll define a Build config ([`build.yaml`](./build.yaml)):

```
apiVersion: shipwright.io/v1alpha1
kind: Build
metadata:
  name: my-build
spec:
  source:
    url: https://github.com/dockersamples/node-bulletin-board
    contextDir: bulletin-board-app
  strategy:
    name: kaniko
    kind: ClusterBuildStrategy
  output:
    image: quay.io/blah/blah
```

```
$ kubectl apply -f build.yaml
```

This build will pull a public GitHub repo, attempt to build it with Kaniko, then fail because it doesn't have push permissions, but that's okay because we can at least see it happen.

Then, we'll define a `Run` config that runs the Build ([`run.yaml`](./run.yaml)):

```
apiVersion: tekton.dev/v1alpha1
kind: Run
metadata:
  generateName: build-run-
spec:
  ref:
    apiVersion: shipwright.io/v1alpha1
    kind: Build
    name: my-build
```

```
$ kubectl create -f run.yaml
```

Now we can see the `Run` progress:

```
$ kubectl get runs -w
NAME              SUCCEEDED   REASON    STARTTIME   COMPLETIONTIME
build-run-sghl7   Unknown     Pending   2s          
build-run-sghl7   Unknown     Running   4s          
build-run-sghl7   False       Failed    7s          0s
```

As expected the `Run` started, ran, and failed.

Let's see the underlying BuildRun it created.

```
$ kubectl get run build-run-sghl7 -ojsonpath="{.status.extraFields.buildRunName}"
build-run-sghl7-buildrun-b5sl5
$ kubectl get buildrun build-run-sghl7-buildrun-b5sl5
NAME                             SUCCEEDED   REASON   STARTTIME   COMPLETIONTIME
build-run-sghl7-buildrun-b5sl5   False       Failed   28s         22s
```

🎉

### Integrate with Tekton Pipelines

Before we can specify a Custom Task in a Pipeline, we need to enable the feature flag:

```
kubectl edit configmap feature-flags -n tekton-pipelines
```

...then edit enable-custom-tasks: 'true'

Next, we'll define a Pipeline that runs a Shipwright Build ([`pipeline.yaml`](./pipeline.yaml)):

```
$ kubectl apply -f pipeline.yaml
```

This Pipeline runs a traditional Tekton Task before the Shipwright Build, and another after it.

Then we'll run the Pipeline using [`tkn`](https://github.com/tektoncd/cli):

```
$ tkn pipeline start -f pipeline.yaml --showlog
PipelineRun started: build-pipeline-run-nrpnc
Waiting for logs to be available...
[before : unnamed-0] + echo before buildrun
[before : unnamed-0] before buildrun
```

This PipelineRun fails because the BuildRun fails, but we can see that it did indeed create the Run:

```
$ kubectl get runs
NAME                                   SUCCEEDED   REASON   STARTTIME   COMPLETIONTIME
build-pipeline-run-nrpnc-build-r7vp2   False       Failed   66s         59s
```

...which created the BuildRun:

```
$ kubectl get buildruns
NAME                                                  SUCCEEDED   REASON   STARTTIME   COMPLETIONTIME
build-pipeline-run-nrpnc-build-r7vp2-buildrun-7dm9g   False       Failed   47s         40s
```
