-- table 名に application prefix を付けない理由と、同居が必要になった場合の分離方針は
-- docs/runtime-platform.md の Migration runner 節が持つ。

-- +goose Up

CREATE TABLE artifact_refs (
    schema text NOT NULL CHECK (schema <> ''),
    digest text NOT NULL CHECK (digest ~ '^sha256:[0-9a-f]{64}$'),
    PRIMARY KEY (schema, digest)
);

CREATE TABLE runs (
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
    -- Escalation Policy は claim 時に pin するが semantic input ではない（ADR-0003）。
    -- 値が変わっても既存 review を stale にしないため、下の identity trigger には入れない。
    -- Run 途中の変更は UPDATE 文へ列を含めないことで防ぐ。
    escalation_policy_schema text NOT NULL,
    escalation_policy_digest text NOT NULL,
    pull_request_ref text,
    fixed_head text NOT NULL DEFAULT '',
    published_head text NOT NULL DEFAULT '',
    published_test_head text NOT NULL DEFAULT '',
    checks_head text NOT NULL DEFAULT '',
    -- 上限の許容範囲は protocol core が持つ。ここで守るのは「gate として意味を成す」下限だけで、
    -- 上限値まで二重管理しない。
    round_limit_test_validity integer NOT NULL CHECK (round_limit_test_validity > 0),
    round_limit_final_implementation integer NOT NULL CHECK (round_limit_final_implementation > 0),
    -- rounds は escalation のたびに 0 へ戻る無人区間の counter、total_rounds は生涯 counter。
    rounds_test_validity integer NOT NULL DEFAULT 0 CHECK (rounds_test_validity >= 0),
    rounds_final_implementation integer NOT NULL DEFAULT 0 CHECK (rounds_final_implementation >= 0),
    total_rounds_test_validity integer NOT NULL DEFAULT 0 CHECK (total_rounds_test_validity >= 0),
    total_rounds_final_implementation integer NOT NULL DEFAULT 0 CHECK (total_rounds_final_implementation >= 0),
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
    CONSTRAINT runs_context_manifest_ref_fkey
        FOREIGN KEY (context_manifest_schema, context_manifest_digest)
        REFERENCES artifact_refs (schema, digest)
        DEFERRABLE INITIALLY DEFERRED,
    CONSTRAINT runs_execution_policy_ref_fkey
        FOREIGN KEY (execution_policy_schema, execution_policy_digest)
        REFERENCES artifact_refs (schema, digest)
        DEFERRABLE INITIALLY DEFERRED,
    CONSTRAINT runs_test_approval_pair CHECK (
        (test_approval_head IS NULL) = (test_approval_request_digest IS NULL)
    ),
    CONSTRAINT runs_escalation_policy_ref_fkey
        FOREIGN KEY (escalation_policy_schema, escalation_policy_digest)
        REFERENCES artifact_refs (schema, digest)
        DEFERRABLE INITIALLY DEFERRED,
    CONSTRAINT runs_final_approval_pair CHECK (
        (final_approval_head IS NULL) = (final_approval_request_digest IS NULL)
    ),
    -- rounds は escalation で 0 へ戻り total_rounds は戻らないので、常に rounds <= total_rounds。
    -- reset 忘れと総数の取りこぼしの両方をここで検出する。
    CONSTRAINT runs_rounds_within_total CHECK (
        rounds_test_validity <= total_rounds_test_validity
        AND rounds_final_implementation <= total_rounds_final_implementation
    )
);

CREATE UNIQUE INDEX runs_one_writer_per_issue
    ON runs (issue_ref)
    WHERE writer_capable;

CREATE TABLE run_transitions (
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
    CONSTRAINT run_transitions_run_fkey
        FOREIGN KEY (run_id) REFERENCES runs (id)
        DEFERRABLE INITIALLY DEFERRED
);

CREATE TABLE run_issue_observations (
    run_id text NOT NULL,
    run_version bigint NOT NULL CHECK (run_version > 0),
    schema text NOT NULL,
    digest text NOT NULL,
    PRIMARY KEY (run_id, run_version),
    CONSTRAINT run_issue_observations_transition_fkey
        FOREIGN KEY (run_id, run_version)
        REFERENCES run_transitions (run_id, version)
        DEFERRABLE INITIALLY DEFERRED,
    CONSTRAINT run_issue_observations_artifact_ref_fkey
        FOREIGN KEY (schema, digest)
        REFERENCES artifact_refs (schema, digest)
        DEFERRABLE INITIALLY DEFERRED
);

-- +goose StatementBegin
CREATE FUNCTION reject_run_input_update()
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
            USING ERRCODE = '23514', CONSTRAINT = 'runs_input_immutable';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER runs_reject_input_update
BEFORE UPDATE ON runs
FOR EACH ROW
EXECUTE FUNCTION reject_run_input_update();

-- +goose StatementBegin
CREATE FUNCTION reject_issue_observation_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'Issue Observation lineage は追記専用です'
        USING ERRCODE = '23514', CONSTRAINT = 'run_issue_observations_append_only';
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER run_issue_observations_append_only
BEFORE UPDATE OR DELETE ON run_issue_observations
FOR EACH ROW
EXECUTE FUNCTION reject_issue_observation_mutation();

-- +goose Down

DROP TRIGGER run_issue_observations_append_only ON run_issue_observations;
DROP FUNCTION reject_issue_observation_mutation();
DROP TRIGGER runs_reject_input_update ON runs;
DROP FUNCTION reject_run_input_update();
DROP TABLE run_issue_observations;
DROP TABLE run_transitions;
DROP TABLE runs;
DROP TABLE artifact_refs;
