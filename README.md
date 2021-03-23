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
