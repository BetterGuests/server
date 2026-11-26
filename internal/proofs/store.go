package proofs

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/BetterGuests/BetterGuests-Server/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const uniqueViolationCode = "23505"

type PostgresProofStore struct {
	DB *pgxpool.Pool
}

func NewPostgresProofStore(pool *pgxpool.Pool) *PostgresProofStore {
	return &PostgresProofStore{DB: pool}
}

func (s *PostgresProofStore) ListingHost(ctx context.Context, actingID, listingID uuid.UUID) (uuid.UUID, error) {
	var hostID uuid.UUID
	err := db.WithUserTx(ctx, s.DB, actingID.String(), func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `SELECT host_id FROM app.listings WHERE id = $1`, listingID).Scan(&hostID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrListingNotFound
		}
		if err != nil {
			return fmt.Errorf("find listing host: %w", err)
		}
		return nil
	})
	return hostID, err
}

func (s *PostgresProofStore) Save(ctx context.Context, proof Proof) error {
	return db.WithUserTx(ctx, s.DB, proof.HostID.String(), func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			INSERT INTO app.property_proofs (
				id, listing_id, host_id, document_type, object_key, content_type, size_bytes,
				address_key, unit_key, status, created_at, updated_at,
				relationship, owner_name, owner_contact, authority_expires_on
			)
			SELECT $1, l.id, l.host_id, $2, $3, $4, $5, l.address_key, l.unit_key, 'submitted', $6, $6,
				$9, NULLIF($10, ''), NULLIF($11, ''), NULLIF($12, '')::date
			FROM app.listings l
			WHERE l.id = $7 AND l.host_id = $8`,
			proof.ID, string(proof.DocumentType), proof.ObjectKey, proof.ContentType, proof.SizeBytes,
			proof.CreatedAt, proof.ListingID, proof.HostID, string(proof.Relationship), proof.OwnerName,
			proof.OwnerContact, proof.ExpiresOn)
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == uniqueViolationCode {
			return ErrProofPending
		}
		if err != nil {
			return fmt.Errorf("insert property proof: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return ErrListingNotFound
		}
		for _, attachment := range proof.Attachments {
			_, err := tx.Exec(ctx, `
				INSERT INTO app.property_proof_attachments (id, proof_id, host_id, kind, object_key, content_type, size_bytes)
				VALUES ($1, $2, $3, $4, $5, $6, $7)`,
				attachment.ID, proof.ID, proof.HostID, string(attachment.Kind), attachment.ObjectKey,
				attachment.ContentType, attachment.SizeBytes)
			if err != nil {
				return fmt.Errorf("insert proof attachment: %w", err)
			}
		}
		return nil
	})
}

const proofColumns = `
	p.id, p.listing_id, p.host_id, p.document_type, CASE WHEN p.file_deleted_at IS NULL THEN coalesce(p.object_key, '') ELSE '' END,
	coalesce(p.content_type, ''),
	coalesce(p.size_bytes, 0),
	p.status, coalesce(p.rejection_reason, ''), p.reviewed_by, p.reviewed_at, p.created_at, p.updated_at,
	p.address_key, p.unit_key,
	p.relationship, coalesce(p.owner_name, ''), coalesce(p.owner_contact, ''),
	coalesce(to_char(p.authority_expires_on, 'YYYY-MM-DD'), '')`

func scanProof(row pgx.Row, extra ...any) (Proof, error) {
	var proof Proof
	var documentType, status, relationship string
	destinations := append([]any{
		&proof.ID, &proof.ListingID, &proof.HostID, &documentType, &proof.ObjectKey, &proof.ContentType,
		&proof.SizeBytes, &status, &proof.RejectionReason, &proof.ReviewedBy, &proof.ReviewedAt,
		&proof.CreatedAt, &proof.UpdatedAt, &proof.AddressKey, &proof.UnitKey,
		&relationship, &proof.OwnerName, &proof.OwnerContact, &proof.ExpiresOn,
	}, extra...)
	if err := row.Scan(destinations...); err != nil {
		return Proof{}, err
	}
	proof.DocumentType = DocumentType(documentType)
	proof.Status = Status(status)
	proof.Relationship = Relationship(relationship)
	return proof, nil
}

func (s *PostgresProofStore) Latest(ctx context.Context, actingID, listingID uuid.UUID) (Proof, bool, error) {
	var proof Proof
	found := false
	err := db.WithUserTx(ctx, s.DB, actingID.String(), func(tx pgx.Tx) error {
		scanned, err := scanProof(tx.QueryRow(ctx, `
			SELECT `+proofColumns+`
			FROM app.property_proofs p
			WHERE p.listing_id = $1 AND p.host_id = $2
			ORDER BY p.created_at DESC, p.id
			LIMIT 1`, listingID, actingID))
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read latest proof: %w", err)
		}
		proof, found = scanned, true
		return nil
	})
	return proof, found, err
}

func (s *PostgresProofStore) IsAdmin(ctx context.Context, accountID uuid.UUID) (bool, error) {
	var admin bool
	err := db.WithUserTx(ctx, s.DB, accountID.String(), func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT app.is_admin()`).Scan(&admin)
	})
	return admin, err
}

func (s *PostgresProofStore) ListSubmitted(ctx context.Context, adminID uuid.UUID) ([]ReviewItem, error) {
	return s.listForReview(ctx, adminID, Submitted)
}

func (s *PostgresProofStore) ListApproved(ctx context.Context, adminID uuid.UUID) ([]ReviewItem, error) {
	return s.listForReview(ctx, adminID, Approved)
}

func (s *PostgresProofStore) listForReview(ctx context.Context, adminID uuid.UUID, status Status) ([]ReviewItem, error) {
	var items []ReviewItem
	err := db.WithUserTx(ctx, s.DB, adminID.String(), func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT `+proofColumns+`,
				l.title, coalesce(l.address_line1, ''), coalesce(l.address_line2, ''), l.city,
				coalesce(l.region, ''), coalesce(l.postal_code, ''), l.country_code,
				coalesce(n.first_name, ''), coalesce(n.middle_name, ''), coalesce(n.last_name, ''), coalesce(n.suffix, '')
			FROM app.property_proofs p
			JOIN app.listings l ON l.id = p.listing_id
			LEFT JOIN LATERAL (
				SELECT first_name, middle_name, last_name, suffix
				FROM app.identity_names
				WHERE account_id = p.host_id
				ORDER BY verified_at DESC, verification_id
				LIMIT 1
			) n ON true
			WHERE p.status = $1
			ORDER BY CASE WHEN p.status = 'submitted' THEN p.created_at END, p.reviewed_at DESC, p.id
			LIMIT 200`, string(status))
		if err != nil {
			return fmt.Errorf("query proofs for review: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var item ReviewItem
			proof, err := scanProof(rows,
				&item.ListingTitle, &item.AddressLine1, &item.AddressLine2, &item.City,
				&item.Region, &item.PostalCode, &item.CountryCode,
				&item.IDFirstName, &item.IDMiddleName, &item.IDLastName, &item.IDNameSuffix)
			if err != nil {
				return fmt.Errorf("scan proof for review: %w", err)
			}
			item.Proof = proof
			items = append(items, item)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		return attachTo(ctx, tx, items)
	})
	return items, err
}

func (s *PostgresProofStore) Decide(ctx context.Context, adminID, proofID uuid.UUID, status Status, reason string, at time.Time) error {
	return db.WithUserTx(ctx, s.DB, adminID.String(), func(tx pgx.Tx) error {
		var hostID uuid.UUID
		var current string
		var ended bool
		err := tx.QueryRow(ctx, `
			SELECT host_id, status, coalesce(authority_expires_on < ($2::timestamptz AT TIME ZONE 'UTC')::date, false)
			FROM app.property_proofs WHERE id = $1`, proofID, at).Scan(&hostID, &current, &ended)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrProofNotFound
		}
		if err != nil {
			return fmt.Errorf("read proof: %w", err)
		}
		if hostID == adminID {
			return ErrOwnListing
		}
		if Status(current) != Submitted {
			return ErrNotAwaitingReview
		}
		if ended && status == Approved {
			return ErrAuthorityExpired
		}
		tag, err := tx.Exec(ctx, `
			UPDATE app.property_proofs
			SET status = $1, reviewed_by = $2, reviewed_at = $3, rejection_reason = NULLIF($4, ''), updated_at = $3
			WHERE id = $5 AND status = 'submitted'`,
			string(status), adminID, at, reason, proofID)
		if err != nil {
			return fmt.Errorf("decide proof: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return ErrNotAwaitingReview
		}
		return nil
	})
}

var revokeOutcomes = map[string]error{
	"done": nil, "forbidden": ErrNotAdmin, "not_found": ErrProofNotFound, "own": ErrOwnListing, "wrong_state": ErrNotApproved,
}

func (s *PostgresProofStore) Revoke(ctx context.Context, adminID, proofID uuid.UUID, reason string, at time.Time) error {
	return db.WithUserTx(ctx, s.DB, adminID.String(), func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO app.accounts (id) VALUES ($1) ON CONFLICT (id) DO NOTHING`, adminID); err != nil {
			return fmt.Errorf("ensure account: %w", err)
		}
		var outcome string
		if err := tx.QueryRow(ctx, `SELECT app.revoke_property_proof($1, $2, $3)`, proofID, reason, at).Scan(&outcome); err != nil {
			return fmt.Errorf("revoke property proof: %w", err)
		}
		mapped, known := revokeOutcomes[outcome]
		if !known {
			return fmt.Errorf("revoke property proof: unknown outcome %q", outcome)
		}
		return mapped
	})
}

func (s *PostgresProofStore) FilesDueForDeletion(ctx context.Context, batchSize int) ([]DueFile, error) {
	rows, err := s.DB.Query(ctx, `SELECT id, object_key FROM app.property_proof_files_due_for_deletion($1)`, batchSize)
	if err != nil {
		return nil, fmt.Errorf("list documents due for deletion: %w", err)
	}
	due, err := pgx.CollectRows(rows, pgx.RowToStructByPos[DueFile])
	if err != nil {
		return nil, fmt.Errorf("read documents due for deletion: %w", err)
	}
	return due, nil
}

func (s *PostgresProofStore) MarkFileDeleted(ctx context.Context, proofID uuid.UUID) error {
	if _, err := s.DB.Exec(ctx, `SELECT app.mark_property_proof_file_deleted($1)`, proofID); err != nil {
		return fmt.Errorf("mark document deleted: %w", err)
	}
	return nil
}

func attachTo(ctx context.Context, tx pgx.Tx, items []ReviewItem) error {
	positions := map[uuid.UUID]int{}
	ids := make([]uuid.UUID, 0, len(items))
	for index, item := range items {
		positions[item.ID] = index
		ids = append(ids, item.ID)
	}
	rows, err := tx.Query(ctx, `
		SELECT proof_id, id, kind, CASE WHEN file_deleted_at IS NULL THEN object_key ELSE '' END
		FROM app.property_proof_attachments
		WHERE proof_id = ANY($1)
		ORDER BY kind`, ids)
	if err != nil {
		return fmt.Errorf("query proof attachments: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var proofID uuid.UUID
		var attachment Attachment
		var kind string
		if err := rows.Scan(&proofID, &attachment.ID, &kind, &attachment.ObjectKey); err != nil {
			return fmt.Errorf("scan proof attachment: %w", err)
		}
		attachment.Kind = AttachmentKind(kind)
		index := positions[proofID]
		items[index].Attachments = append(items[index].Attachments, attachment)
	}
	return rows.Err()
}

func (s *PostgresProofStore) ExpireAuthority(ctx context.Context, today time.Time) (int, error) {
	var expired int
	if err := s.DB.QueryRow(ctx, `SELECT app.expire_property_authority($1::date)`, today.UTC().Format(time.DateOnly)).Scan(&expired); err != nil {
		return 0, fmt.Errorf("end expired listing permissions: %w", err)
	}
	return expired, nil
}
