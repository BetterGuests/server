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
				address_key, unit_key, status, created_at, updated_at
			)
			SELECT $1, l.id, l.host_id, $2, $3, $4, $5, l.address_key, l.unit_key, 'submitted', $6, $6
			FROM app.listings l
			WHERE l.id = $7 AND l.host_id = $8`,
			proof.ID, string(proof.DocumentType), proof.ObjectKey, proof.ContentType, proof.SizeBytes,
			proof.CreatedAt, proof.ListingID, proof.HostID)
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
		return nil
	})
}

const proofColumns = `
	p.id, p.listing_id, p.host_id, p.document_type, p.object_key, p.content_type, p.size_bytes,
	p.status, coalesce(p.rejection_reason, ''), p.reviewed_by, p.reviewed_at, p.created_at, p.updated_at,
	p.extra_object_keys, p.address_key, p.unit_key`

func scanProof(row pgx.Row, extra ...any) (Proof, error) {
	var proof Proof
	var documentType, status string
	destinations := append([]any{
		&proof.ID, &proof.ListingID, &proof.HostID, &documentType, &proof.ObjectKey, &proof.ContentType,
		&proof.SizeBytes, &status, &proof.RejectionReason, &proof.ReviewedBy, &proof.ReviewedAt,
		&proof.CreatedAt, &proof.UpdatedAt, &proof.ExtraObjectKeys, &proof.AddressKey, &proof.UnitKey,
	}, extra...)
	if err := row.Scan(destinations...); err != nil {
		return Proof{}, err
	}
	proof.DocumentType = DocumentType(documentType)
	proof.Status = Status(status)
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
			WHERE p.status = 'submitted'
			ORDER BY p.created_at, p.id
			LIMIT 200`)
		if err != nil {
			return fmt.Errorf("query submitted proofs: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var item ReviewItem
			proof, err := scanProof(rows,
				&item.ListingTitle, &item.AddressLine1, &item.AddressLine2, &item.City,
				&item.Region, &item.PostalCode, &item.CountryCode,
				&item.IDFirstName, &item.IDMiddleName, &item.IDLastName, &item.IDNameSuffix)
			if err != nil {
				return fmt.Errorf("scan submitted proof: %w", err)
			}
			item.Proof = proof
			items = append(items, item)
		}
		return rows.Err()
	})
	return items, err
}

func (s *PostgresProofStore) Decide(ctx context.Context, adminID, proofID uuid.UUID, status Status, reason string, at time.Time) error {
	return db.WithUserTx(ctx, s.DB, adminID.String(), func(tx pgx.Tx) error {
		var hostID uuid.UUID
		var current string
		err := tx.QueryRow(ctx, `SELECT host_id, status FROM app.property_proofs WHERE id = $1`, proofID).Scan(&hostID, &current)
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

func (s *PostgresProofStore) CheckSubject(ctx context.Context, hostID, listingID uuid.UUID) (CheckSubject, error) {
	var subject CheckSubject
	err := db.WithUserTx(ctx, s.DB, hostID.String(), func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `
			SELECT coalesce(address_line1, ''), coalesce(address_line2, ''), city, coalesce(postal_code, ''),
				country_code, address_key, unit_key
			FROM app.listings
			WHERE id = $1 AND host_id = $2`, listingID, hostID).Scan(
			&subject.Address.Line1, &subject.Address.Line2, &subject.Address.City, &subject.Address.PostalCode,
			&subject.Address.CountryCode, &subject.AddressKey, &subject.UnitKey)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrListingNotFound
		}
		if err != nil {
			return fmt.Errorf("read listing address: %w", err)
		}
		return nil
	})
	return subject, err
}

func (s *PostgresProofStore) IDName(ctx context.Context, hostID uuid.UUID) (IDName, bool, error) {
	var name IDName
	found := false
	err := db.WithUserTx(ctx, s.DB, hostID.String(), func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `
			SELECT first_name, last_name, suffix
			FROM app.identity_names
			WHERE account_id = $1
			ORDER BY verified_at DESC, verification_id
			LIMIT 1`, hostID).Scan(&name.First, &name.Last, &name.Suffix)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read ID name: %w", err)
		}
		found = true
		return nil
	})
	return name, found, err
}

func (s *PostgresProofStore) SaveInquiry(ctx context.Context, inquiry CheckInquiry) error {
	return db.WithUserTx(ctx, s.DB, inquiry.HostID.String(), func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO app.property_proof_inquiries (
				id, persona_inquiry_id, listing_id, host_id, document_type, address_key, unit_key, created_at
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
			inquiry.ID, inquiry.InquiryID, inquiry.ListingID, inquiry.HostID, string(inquiry.DocumentType),
			inquiry.AddressKey, inquiry.UnitKey, inquiry.CreatedAt)
		if err != nil {
			return fmt.Errorf("insert document inquiry: %w", err)
		}
		return nil
	})
}

func (s *PostgresProofStore) FindInquiry(ctx context.Context, hostID uuid.UUID, inquiryID string) (CheckInquiry, bool, error) {
	var inquiry CheckInquiry
	found := false
	err := db.WithUserTx(ctx, s.DB, hostID.String(), func(tx pgx.Tx) error {
		var documentType string
		err := tx.QueryRow(ctx, `
			SELECT id, persona_inquiry_id, listing_id, host_id, document_type, address_key, unit_key, created_at
			FROM app.property_proof_inquiries
			WHERE persona_inquiry_id = $1 AND host_id = $2`, inquiryID, hostID).Scan(
			&inquiry.ID, &inquiry.InquiryID, &inquiry.ListingID, &inquiry.HostID, &documentType,
			&inquiry.AddressKey, &inquiry.UnitKey, &inquiry.CreatedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("find document inquiry: %w", err)
		}
		inquiry.DocumentType = DocumentType(documentType)
		found = true
		return nil
	})
	return inquiry, found, err
}

func (s *PostgresProofStore) HasProofForInquiry(ctx context.Context, hostID uuid.UUID, inquiryID string) (bool, error) {
	var exists bool
	err := db.WithUserTx(ctx, s.DB, hostID.String(), func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM app.property_proofs WHERE persona_inquiry_id = $1 AND host_id = $2)`,
			inquiryID, hostID).Scan(&exists)
	})
	if err != nil {
		return false, fmt.Errorf("check document inquiry proof: %w", err)
	}
	return exists, nil
}

func (s *PostgresProofStore) SaveFromInquiry(ctx context.Context, proof Proof) error {
	extra := proof.ExtraObjectKeys
	if extra == nil {
		extra = []string{}
	}
	return db.WithUserTx(ctx, s.DB, proof.HostID.String(), func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			INSERT INTO app.property_proofs (
				id, listing_id, host_id, document_type, object_key, content_type, size_bytes,
				address_key, unit_key, status, created_at, updated_at, persona_inquiry_id, extra_object_keys
			)
			SELECT $1, l.id, l.host_id, $2, $3, $4, $5, $6, $7, 'submitted', $8, $8, $9, $10
			FROM app.listings l
			WHERE l.id = $11 AND l.host_id = $12`,
			proof.ID, string(proof.DocumentType), proof.ObjectKey, proof.ContentType, proof.SizeBytes,
			proof.AddressKey, proof.UnitKey, proof.CreatedAt, proof.InquiryID, extra, proof.ListingID, proof.HostID)
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == uniqueViolationCode {
			return ErrProofPending
		}
		if err != nil {
			return fmt.Errorf("insert checked property proof: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return ErrListingNotFound
		}
		return nil
	})
}

func (s *PostgresProofStore) ApproveFromInquiry(ctx context.Context, hostID uuid.UUID, inquiryID string, at time.Time) (bool, error) {
	var approved bool
	err := db.WithUserTx(ctx, s.DB, hostID.String(), func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT app.approve_persona_property_proof($1, $2)`, inquiryID, at).Scan(&approved)
	})
	if err != nil {
		return false, fmt.Errorf("approve checked property proof: %w", err)
	}
	return approved, nil
}
