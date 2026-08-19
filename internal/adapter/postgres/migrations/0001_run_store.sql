CREATE TABLE kudo_schema_migrations (
    version integer PRIMARY KEY,
    name text NOT NULL,
    checksum text NOT NULL CHECK (checksum ~ '^sha256:[0-9a-f]{64}$'),
    applied_at timestamptz NOT NULL DEFAULT current_timestamp
);

CREATE TABLE kudo_artifact_refs (
    schema text NOT NULL CHECK (schema <> ''),
    digest text NOT NULL CHECK (digest ~ '^sha256:[0-9a-f]{64}$'),
    PRIMARY KEY (schema, digest)
);

CREATE TABLE kudo_runs (
    id text PRIMARY KEY CHECK (id <> ''),
    issue_ref text NOT NULL CHECK (issue_ref <> ''),
    version bigint NOT NULL CHECK (version > 0),
    phase text NOT NULL CHECK (phase IN (
        'claimed',
        'authoring_tests',
        'publishing_test_head',
        'awaiting_test_review',
        'implementing',
        'publishing_final_head',
        'awaiting_final_review',
        'finalizing_pull_request',
        'awaiting_human_review',
        'needs_human',
        'superseded'
    )),
    context_manifest_schema text NOT NULL,
    context_manifest_digest text NOT NULL,
    execution_policy_schema text NOT NULL,
    execution_policy_digest text NOT NULL,
    pull_request_ref text,
    fixed_head text NOT NULL DEFAULT '',
    published_head text NOT NULL DEFAULT '',
    published_test_head text NOT NULL DEFAULT '',
    checks_head text NOT NULL DEFAULT '',
    test_approval_head text,
    test_approval_request_digest text CHECK (
        test_approval_request_digest IS NULL
        OR test_approval_request_digest ~ '^sha256:[0-9a-f]{64}$'
    ),
    final_approval_head text,
    final_approval_request_digest text CHECK (
        final_approval_request_digest IS NULL
        OR final_approval_request_digest ~ '^sha256:[0-9a-f]{64}$'
    ),
    writer_capable boolean GENERATED ALWAYS AS (phase IN (
        'claimed',
        'authoring_tests',
        'publishing_test_head',
        'awaiting_test_review',
        'implementing',
        'publishing_final_head',
        'awaiting_final_review',
        'finalizing_pull_request'
    )) STORED,
    CONSTRAINT kudo_runs_context_manifest_ref_fkey
        FOREIGN KEY (context_manifest_schema, context_manifest_digest)
        REFERENCES kudo_artifact_refs (schema, digest)
        DEFERRABLE INITIALLY DEFERRED,
    CONSTRAINT kudo_runs_execution_policy_ref_fkey
        FOREIGN KEY (execution_policy_schema, execution_policy_digest)
        REFERENCES kudo_artifact_refs (schema, digest)
        DEFERRABLE INITIALLY DEFERRED,
    CONSTRAINT kudo_runs_test_approval_pair CHECK (
        (test_approval_head IS NULL) = (test_approval_request_digest IS NULL)
    ),
    CONSTRAINT kudo_runs_final_approval_pair CHECK (
        (final_approval_head IS NULL) = (final_approval_request_digest IS NULL)
    )
);

CREATE UNIQUE INDEX kudo_runs_one_writer_per_issue
    ON kudo_runs (issue_ref)
    WHERE writer_capable;

CREATE TABLE kudo_run_transitions (
    run_id text NOT NULL,
    version bigint NOT NULL CHECK (version > 0),
    event_kind text NOT NULL CHECK (event_kind IN (
        'claim_succeeded',
        'operation_started',
        'tests_authored',
        'head_published',
        'review_completed',
        'implementation_fixed',
        'pull_request_finalized',
        'observation_recorded',
        'semantic_input_changed',
        'attempt_failed',
        'human_escalated'
    )),
    from_phase text CHECK (
        from_phase IS NULL OR from_phase = '' OR from_phase IN (
            'claimed',
            'authoring_tests',
            'publishing_test_head',
            'awaiting_test_review',
            'implementing',
            'publishing_final_head',
            'awaiting_final_review',
            'finalizing_pull_request',
            'awaiting_human_review',
            'needs_human',
            'superseded'
        )
    ),
    to_phase text NOT NULL CHECK (to_phase IN (
        'claimed',
        'authoring_tests',
        'publishing_test_head',
        'awaiting_test_review',
        'implementing',
        'publishing_final_head',
        'awaiting_final_review',
        'finalizing_pull_request',
        'awaiting_human_review',
        'needs_human',
        'superseded'
    )),
    PRIMARY KEY (run_id, version),
    CONSTRAINT kudo_run_transitions_run_fkey
        FOREIGN KEY (run_id) REFERENCES kudo_runs (id)
        DEFERRABLE INITIALLY DEFERRED
);

CREATE TABLE kudo_run_issue_observations (
    run_id text NOT NULL,
    run_version bigint NOT NULL CHECK (run_version > 0),
    schema text NOT NULL,
    digest text NOT NULL,
    PRIMARY KEY (run_id, run_version),
    CONSTRAINT kudo_run_issue_observations_transition_fkey
        FOREIGN KEY (run_id, run_version)
        REFERENCES kudo_run_transitions (run_id, version)
        DEFERRABLE INITIALLY DEFERRED,
    CONSTRAINT kudo_run_issue_observations_artifact_ref_fkey
        FOREIGN KEY (schema, digest)
        REFERENCES kudo_artifact_refs (schema, digest)
        DEFERRABLE INITIALLY DEFERRED
);

CREATE FUNCTION kudo_reject_run_input_update()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF (
        ROW(OLD.context_manifest_schema, OLD.context_manifest_digest)
            IS DISTINCT FROM ROW(NEW.context_manifest_schema, NEW.context_manifest_digest)
        OR ROW(OLD.execution_policy_schema, OLD.execution_policy_digest)
            IS DISTINCT FROM ROW(NEW.execution_policy_schema, NEW.execution_policy_digest)
    ) THEN
        RAISE EXCEPTION 'Run の semantic input は変更できません'
            USING ERRCODE = '23514', CONSTRAINT = 'kudo_runs_input_immutable';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER kudo_runs_reject_input_update
BEFORE UPDATE ON kudo_runs
FOR EACH ROW
EXECUTE FUNCTION kudo_reject_run_input_update();

CREATE FUNCTION kudo_reject_issue_observation_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'Issue Observation lineage は追記専用です'
        USING ERRCODE = '23514', CONSTRAINT = 'kudo_run_issue_observations_append_only';
END;
$$;

CREATE TRIGGER kudo_run_issue_observations_append_only
BEFORE UPDATE OR DELETE ON kudo_run_issue_observations
FOR EACH ROW
EXECUTE FUNCTION kudo_reject_issue_observation_mutation();
