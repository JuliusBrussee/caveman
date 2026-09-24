package store

import (
	"database/sql"
	"errors"
)

// Middleware originals are stored once per (authority, digest): the row is both
// the exact original a grant recovers and the unique-content credit. A row with
// no body is a credit protocol 1.0 wrote while its original lived in CCR. The
// body is whatever the runtime handed over (sealed when a key is configured);
// key_id names the key, empty for plaintext.

// SaveOriginal stores body in the transaction that publishes the plan using it,
// so an aborted or not_smaller plan leaves no original behind. It reports
// whether the authority saw this digest for the first time (the credit). A
// credit-only row gets its body filled in without a second credit.
func (t *sqliteMiddlewareTx) SaveOriginal(authority, digest string, body []byte, keyID string) (bool, error) {
	var stored bool
	err := t.tx.QueryRowContext(t.ctx, `SELECT body IS NOT NULL FROM middleware_originals WHERE authority=? AND digest=?`, authority, digest).Scan(&stored)
	if err == nil {
		if stored || body == nil {
			return false, nil
		}
		if err := t.capacity(len(body), 0); err != nil {
			return false, err
		}
		_, err = t.tx.ExecContext(t.ctx, `UPDATE middleware_originals SET body=?,key_id=? WHERE authority=? AND digest=?`, body, keyID, authority, digest)
		return false, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	if err = t.capacity(64+len(body), 1); err != nil {
		return false, err
	}
	_, err = t.tx.ExecContext(t.ctx, `INSERT INTO middleware_originals(authority,digest,body,key_id,principal) VALUES (?,?,?,?,?)`,
		authority, digest, body, keyID, t.Principal)
	return err == nil, err
}

// CreditOriginal counts identical content once inside an authenticated session
// scope, without storing it or merging document/source identities or grants.
func (t *MiddlewareTx) CreditOriginal(authority, digest string) (bool, error) {
	return t.SaveOriginal(authority, digest, nil, "")
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

// HasOriginal reports whether Original would find content, without reading it.
func (t *sqliteMiddlewareTx) HasOriginal(authority, digest string) (bool, error) {
	var found int
	err := t.tx.QueryRowContext(t.ctx, `SELECT 1 FROM middleware_originals WHERE authority=? AND digest=? AND body IS NOT NULL`,
		authority, digest).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}
