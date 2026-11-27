package proofscan

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

type PostgresStore struct {
	DB          *pgxpool.Pool
	AutoApprove bool
}

func NewPostgresStore(pool *pgxpool.Pool, autoApprove bool) *PostgresStore {
	return &PostgresStore{DB: pool, AutoApprove: autoApprove}
}

func (store *PostgresStore) DueForScan(ctx context.Context, limit int) ([]Due, error) {
	rows, err := store.DB.Query(ctx, `
		SELECT id, object_key, content_type, owner_name, id_name, address_line1, city, postal_code
		FROM app.property_proofs_due_for_scan($1)`, limit)
	if err != nil {
		return nil, fmt.Errorf("query proofs due for scan: %w", err)
	}
	defer rows.Close()
	var due []Due
	for rows.Next() {
		var proof Due
		if err := rows.Scan(&proof.ProofID, &proof.ObjectKey, &proof.ContentType, &proof.OwnerName, &proof.IDName,
			&proof.Address.Line1, &proof.Address.City, &proof.Address.PostalCode); err != nil {
			return nil, fmt.Errorf("scan proof due for scan: %w", err)
		}
		due = append(due, proof)
	}
	return due, rows.Err()
}

const autoApprovedOutcome = "approved"

func (store *PostgresStore) Record(ctx context.Context, result Result) (bool, error) {
	var outcome string
	err := store.DB.QueryRow(ctx, `SELECT app.record_property_proof_scan($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		result.ProofID, result.FoundName, string(result.NameVerdict), result.AddressMatched,
		result.Excerpt, result.Error, result.ScannedAt, result.DocumentDate, store.AutoApprove).Scan(&outcome)
	if err != nil {
		return false, fmt.Errorf("record proof scan: %w", err)
	}
	return outcome == autoApprovedOutcome, nil
}

func (store *PostgresStore) ForgetDeletedText(ctx context.Context) (int, error) {
	var cleared int
	if err := store.DB.QueryRow(ctx, `SELECT app.forget_property_proof_scan_text()`).Scan(&cleared); err != nil {
		return 0, fmt.Errorf("forget scan text: %w", err)
	}
	return cleared, nil
}
