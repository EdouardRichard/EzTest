-- 0.2.1 version context: the workflow stage a version was suspended from.
-- blocked/paused are suspension states orthogonal to the workflow. The stage
-- machine writes the current stage here when entering blocked/paused and clears
-- it when resuming, so a resume returns only to the recorded stage and cannot
-- jump ahead past a hard gate. NULL means the version is not suspended.
-- Additive only: no 0.2/0.2.1 row is rewritten.
-- See docs/version-V0.2.1/spec.md §5.3, §5.5.

ALTER TABLE version
    ADD COLUMN suspended_from TEXT NULL
        CONSTRAINT chk_version_suspended_from CHECK (
            suspended_from IS NULL OR suspended_from IN (
                'created', 'clarifying', 'requirement_review', 'designing',
                'case_review', 'executing', 'reporting'
            )
        );
