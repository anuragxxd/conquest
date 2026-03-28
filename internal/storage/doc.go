// Package storage defines the object storage layout conventions for the
// Conquest workflow execution platform.
//
// # Layout Version: v1
//
// Every run's artifacts live under a single, deletable prefix:
//
//	s3://<bucket>/v1/workflow-runs/<run-id>/
//	  request/
//	    run-request.json           Original WES RunRequest (verbatim JSON)
//	    resolved-workflow.json     Resolved workflow reference (TRS URI, etc.)
//	    resolved-inputs.json       Resolved input metadata
//	  workflow/
//	    bundle/                    Workflow files (from TRS or upload)
//	  work/
//	    nextflow/                  Nextflow's work tree (engine-managed)
//	    snakemake/                 Snakemake's work tree (engine-managed, Phase 3)
//	  logs/
//	    run.log                    Overall run log
//	    tasks/<task-id>/
//	      attempt-000/
//	        executor-0.log         Per-executor log (supports retry + multi-executor)
//	  outputs/                     Final workflow outputs
//	  metadata/
//	    run.json                   Run state snapshot
//	    tasks/<task-id>.json       Task state snapshots
//
// # Zones
//
// Platform-managed zones: request/, logs/, outputs/, metadata/. The platform
// reads and writes here.
//
// Engine-managed zones: work/nextflow/, work/snakemake/. The platform only
// provides the URI as a launch parameter; the engine owns everything inside.
// For Nextflow, pass NextflowWorkDirURI as the -work-dir argument.
//
// # URI vs Key
//
// Methods ending in "URI" return full s3:// URIs suitable for Nextflow,
// TES inputs/outputs, and external references. Methods without "URI"
// return bare S3 keys (no bucket prefix) suitable for SDK calls.
//
// # Persistence
//
// Each run persists its layout_version and run_root_uri in Postgres so
// that old runs remain resolvable even if the Layout configuration changes
// later.
package storage
