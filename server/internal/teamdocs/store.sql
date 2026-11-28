CREATE TABLE IF NOT EXISTS teamdocs_members (
    project text NOT NULL,
    member text NOT NULL,
    role text NOT NULL CHECK (role IN ('reader', 'writer', 'approver')),
    token_hash bytea NOT NULL UNIQUE CHECK (octet_length(token_hash) = 32),
    PRIMARY KEY (project, member)
);

CREATE TABLE IF NOT EXISTS teamdocs_heads (
    project text NOT NULL,
    document text NOT NULL,
    head bigint NOT NULL CHECK (head >= 0),
    PRIMARY KEY (project, document)
);

CREATE TABLE IF NOT EXISTS teamdocs_revisions (
    project text NOT NULL,
    document text NOT NULL,
    revision bigint NOT NULL CHECK (revision > 0),
    content bytea NOT NULL,
    sha256 text NOT NULL,
    author text NOT NULL,
    approved_by text,
    approved_at timestamptz,
    PRIMARY KEY (project, document, revision),
    FOREIGN KEY (project, document) REFERENCES teamdocs_heads (project, document)
);

CREATE INDEX IF NOT EXISTS teamdocs_approved_revision
    ON teamdocs_revisions (project, document, revision DESC) WHERE approved_by IS NOT NULL;

-- Use the same bytewise order for document cursors across database locales.
CREATE INDEX IF NOT EXISTS teamdocs_document_catalog
    ON teamdocs_heads (project, document COLLATE "C");
