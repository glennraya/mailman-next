package mailstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// schemaVersion is the migration the code expects. Bump it and add a case to
// applyVersion; never edit a migration that has shipped.
const schemaVersion = 1

var migrations = map[int][]string{
	1: {
		`CREATE TABLE conversations (
			id               INTEGER PRIMARY KEY AUTOINCREMENT,
			subject          TEXT NOT NULL DEFAULT '',
			subject_key      TEXT NOT NULL DEFAULT '',
			last_activity_at TEXT NOT NULL,
			created_at       TEXT NOT NULL
		)`,
		`CREATE INDEX conversations_subject_key ON conversations (subject_key)`,
		`CREATE INDEX conversations_last_activity ON conversations (last_activity_at DESC)`,

		// id is a ULID: sortable by creation time, so paging by id needs no
		// secondary sort and stays stable when two messages share a second.
		`CREATE TABLE messages (
			id              TEXT PRIMARY KEY,
			conversation_id INTEGER NOT NULL REFERENCES conversations (id) ON DELETE CASCADE,
			direction       TEXT NOT NULL DEFAULT 'inbound',
			message_id      TEXT NOT NULL DEFAULT '',
			in_reply_to     TEXT NOT NULL DEFAULT '',
			refs            TEXT,
			from_addr       TEXT,
			to_addr         TEXT,
			cc_addr         TEXT,
			bcc_addr        TEXT,
			reply_to_addr   TEXT,
			envelope_from   TEXT NOT NULL DEFAULT '',
			envelope_rcpt   TEXT,
			subject         TEXT NOT NULL DEFAULT '',
			sent_at         TEXT,
			text_body       TEXT NOT NULL DEFAULT '',
			html_body       TEXT NOT NULL DEFAULT '',
			size_bytes      INTEGER NOT NULL DEFAULT 0,
			seen_at         TEXT,
			raw_path        TEXT NOT NULL DEFAULT '',
			created_at      TEXT NOT NULL
		)`,
		`CREATE INDEX messages_conversation ON messages (conversation_id, id)`,
		`CREATE INDEX messages_message_id ON messages (message_id)`,
		`CREATE INDEX messages_unseen ON messages (seen_at) WHERE seen_at IS NULL`,

		`CREATE TABLE attachments (
			id           INTEGER PRIMARY KEY AUTOINCREMENT,
			message_id   TEXT NOT NULL REFERENCES messages (id) ON DELETE CASCADE,
			filename     TEXT NOT NULL DEFAULT '',
			content_type TEXT NOT NULL DEFAULT 'application/octet-stream',
			content_id   TEXT NOT NULL DEFAULT '',
			inline       INTEGER NOT NULL DEFAULT 0,
			size_bytes   INTEGER NOT NULL DEFAULT 0,
			path         TEXT NOT NULL
		)`,
		`CREATE INDEX attachments_message ON attachments (message_id)`,
		`CREATE INDEX attachments_content_id ON attachments (content_id)`,

		`CREATE TABLE deliveries (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			message_id  TEXT NOT NULL REFERENCES messages (id) ON DELETE CASCADE,
			target_url  TEXT NOT NULL DEFAULT '',
			format      TEXT NOT NULL DEFAULT '',
			attempt     INTEGER NOT NULL DEFAULT 1,
			status_code INTEGER NOT NULL DEFAULT 0,
			error       TEXT NOT NULL DEFAULT '',
			duration_ms INTEGER NOT NULL DEFAULT 0,
			created_at  TEXT NOT NULL
		)`,
		`CREATE INDEX deliveries_message ON deliveries (message_id, id)`,
	},
}

// migrate walks the database from whatever version it is at up to the current
// one. Each step runs in its own transaction, so a failure leaves the version
// counter pointing at the last step that fully applied.
func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx,
		`CREATE TABLE IF NOT EXISTS schema_version (version INTEGER NOT NULL)`); err != nil {
		return fmt.Errorf("create schema_version: %w", err)
	}

	current, err := s.currentVersion(ctx)
	if err != nil {
		return err
	}
	if current > schemaVersion {
		return fmt.Errorf(
			"database is at schema version %d but this build understands %d; upgrade Mailman",
			current, schemaVersion)
	}

	for version := current + 1; version <= schemaVersion; version++ {
		if err := s.applyVersion(ctx, version); err != nil {
			return fmt.Errorf("apply schema version %d: %w", version, err)
		}
	}
	return nil
}

func (s *Store) applyVersion(ctx context.Context, version int) error {
	statements, ok := migrations[version]
	if !ok {
		return errors.New("no such schema version")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("%w: %s", err, statement)
		}
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM schema_version`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO schema_version (version) VALUES (?)`, version); err != nil {
		return err
	}

	return tx.Commit()
}

// currentVersion reports 0 for a database that has never been migrated.
func (s *Store) currentVersion(ctx context.Context) (int, error) {
	var version int
	err := s.db.QueryRowContext(ctx,
		`SELECT version FROM schema_version LIMIT 1`).Scan(&version)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read schema version: %w", err)
	}
	return version, nil
}
