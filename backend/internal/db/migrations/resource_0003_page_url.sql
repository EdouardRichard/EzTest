-- 0.2.1 resource context: sut_page uniqueness moves from route_template to a
-- normalized URL. 0.2.1 starts from a fresh database, so no 0.2 row needs a
-- backfill; the generated column keeps every future insert normalized.
--
-- Normalization rule (must match page discovery when it lands):
--   * lowercase the scheme and host
--   * strip default ports (:80 for http, :443 for https)
--   * keep the path case-sensitive; drop trailing slashes except for root "/"
--   * keep the fragment (SPA routes live there)
--   * drop the query string
-- The userinfo component is preserved. Non-absolute values are returned
-- trimmed (sut_page.url is validated as an absolute http(s) URL upstream).

CREATE FUNCTION eztest_normalize_url(raw TEXT) RETURNS TEXT
LANGUAGE plpgsql IMMUTABLE PARALLEL SAFE
AS $$
DECLARE
    trimmed    TEXT := btrim(raw);
    matched    TEXT[];
    scheme     TEXT;
    authority  TEXT;
    host_port  TEXT;
    host_match TEXT[];
    host       TEXT;
    port       TEXT;
    path       TEXT;
    fragment   TEXT;
    at_pos     INT;
BEGIN
    IF trimmed IS NULL OR trimmed = '' THEN
        RETURN trimmed;
    END IF;

    matched := regexp_match(
        trimmed,
        '^([A-Za-z][A-Za-z0-9+.-]*)://([^/?#]*)([^?#]*)(?:\?[^#]*)?(#.*)?$'
    );
    IF matched IS NULL THEN
        RETURN trimmed;
    END IF;

    scheme := lower(matched[1]);
    authority := matched[2];
    path := coalesce(matched[3], '');
    fragment := coalesce(matched[4], '');

    at_pos := strpos(authority, '@');
    IF at_pos > 0 THEN
        host_port := substr(authority, at_pos + 1);
    ELSE
        host_port := authority;
    END IF;

    host_match := regexp_match(host_port, '^(.*):([0-9]+)$');
    IF host_match IS NOT NULL THEN
        host := host_match[1];
        port := host_match[2];
    ELSE
        host := host_port;
        port := '';
    END IF;

    IF (scheme = 'http' AND port = '80') OR (scheme = 'https' AND port = '443') THEN
        port := '';
    END IF;

    authority := lower(host) || CASE WHEN port <> '' THEN ':' || port ELSE '' END;
    IF at_pos > 0 THEN
        authority := substr(matched[2], 1, at_pos) || authority;
    END IF;

    IF path = '' THEN
        path := '/';
    ELSE
        path := regexp_replace(path, '/+$', '');
        IF path = '' THEN
            path := '/';
        END IF;
    END IF;

    RETURN scheme || '://' || authority || path || fragment;
END;
$$;

ALTER TABLE sut_page
    ALTER COLUMN route_template DROP NOT NULL,
    ADD COLUMN normalized_url TEXT
        GENERATED ALWAYS AS (eztest_normalize_url(url)) STORED NOT NULL;

DROP INDEX IF EXISTS uk_sut_page_template;
CREATE UNIQUE INDEX uk_sut_page_normalized_url
    ON sut_page (environment_id, normalized_url);
