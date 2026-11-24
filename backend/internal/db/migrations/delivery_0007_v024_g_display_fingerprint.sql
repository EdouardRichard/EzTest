-- v0.2.4-G (T11 fix round 1, SC-G8 / R-G10a): display projection digest on the
-- delivery report header. display_fingerprint hashes the DISPLAY-ONLY state the
-- published report shows (design judge review_state/verdict/findings/candidate
-- matrix) INDEPENDENTLY of the canonical input_fingerprint (review_state is an
-- instant display projection, art.V.11, deliberately NOT a canonical member).
-- The complete stale sweep compares it against the current source display state
-- so a lost judge event on a static version still heals (art.7.6, no silent
-- stale display). Append-only: existing rows default to '' (the first sweep
-- refresh treats an empty digest as divergent and republishes once).
ALTER TABLE delivery_report
    ADD COLUMN display_fingerprint TEXT NOT NULL DEFAULT '';