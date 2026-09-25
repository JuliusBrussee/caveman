package store

import (
	"database/sql"
	"errors"
)

// Middleware originals are stored once per (authority, digest): the row is both
// the exact original a grant recovers and the unique-content credit. A row with
// no body is a credit protocol 1.0 wrote while its original lived in CCR, or one
// whose original expiry reclaimed while its authority lived on. The body is
// whatever the runtime handed over (sealed when a key is configured); key_id
// names the key, empty for plaintext. SaveOriginals (middleware.go) decides
// which rows to insert or update.

// Originals looks each digest up in turn (in process: no round trips).
func (t *sqliteMiddlewareTx) Originals(authority string, digests []string) (map[string]StoredOriginal, error) {
	found := map[string]StoredOriginal{}
	for _, digest := range digests {
		var o StoredOriginal
		err := t.tx.QueryRowContext(t.ctx, `SELECT key_id,body IS NOT NULL FROM middleware_originals WHERE authority=? AND digest=?`, authority, digest).Scan(&o.KeyID, &o.Body)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		found[digest] = o
	}
	return found, nil
}

func (t *sqliteMiddlewareTx) insertOriginals(authority string, originals []MiddlewareOriginal) error {
	t.grew = true
	for _, o := range originals {
		if _, err := t.tx.ExecContext(t.ctx, `INSERT INTO middleware_originals(authority,digest,body,key_id,principal) VALUES (?,?,?,?,?)`,
			authority, o.Digest, o.Body, o.KeyID, t.Principal); err != nil {
			return err
		}
	}
	return nil
}

// updateOriginals stores bodies over existing rows and stamps the principal: a
// protocol 1.0 credit has none, and its body must count against the quota of
// the principal that filled it.
func (t *sqliteMiddlewareTx) updateOriginals(authority string, originals []MiddlewareOriginal) error {
	t.grew = true
	for _, o := range originals {
		if _, err := t.tx.ExecContext(t.ctx, `UPDATE middleware_originals SET body=?,key_id=?,principal=? WHERE authority=? AND digest=?`,
			o.Body, o.KeyID, t.Principal, authority, o.Digest); err != nil {
			return err
		}
	}
	return nil
}

// Original returns a stored original and its key id; sql.ErrNoRows when the
// authority holds no content for digest.
func (t *sqliteMiddlewareTx) Original(authority, digest string) ([]byte, string, error) {
	var body []byte
	var keyID string
	err := t.tx.QueryRowContext(t.ctx, `SELECT body,key_id FROM middleware_originals WHERE authority=? AND digest=? AND body IS NOT NULL`,
		authority, digest).Scan(&body, &keyID)
	return body, keyID, err
}
