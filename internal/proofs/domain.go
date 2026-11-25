package proofs

import (
	"context"
	"time"

	"github.com/google/uuid"
)

type DocumentType string

const (
	UtilityBill     DocumentType = "utility_bill"
	Deed            DocumentType = "deed"
	Lease           DocumentType = "lease"
	PropertyTaxBill DocumentType = "property_tax_bill"
)

type Status string

const (
	Submitted Status = "submitted"
	Approved  Status = "approved"
	Rejected  Status = "rejected"
)

type Decision string

const (
	Approve Decision = "approve"
	Reject  Decision = "reject"
)

type Proof struct {
	ID              uuid.UUID    `json:"id"`
	ListingID       uuid.UUID    `json:"listing_id"`
	HostID          uuid.UUID    `json:"host_id"`
	DocumentType    DocumentType `json:"document_type"`
	ContentType     string       `json:"content_type"`
	SizeBytes       int64        `json:"size_bytes"`
	Status          Status       `json:"status"`
	RejectionReason string       `json:"rejection_reason,omitempty"`
	ReviewedBy      *uuid.UUID   `json:"reviewed_by,omitempty"`
	ReviewedAt      *time.Time   `json:"reviewed_at,omitempty"`
	CreatedAt       time.Time    `json:"created_at"`
	UpdatedAt       time.Time    `json:"updated_at"`
	InquiryID       string       `json:"persona_inquiry_id,omitempty"`
	ObjectKey       string       `json:"-"`
	AddressKey      *string      `json:"-"`
	UnitKey         *string      `json:"-"`
}

type ReviewItem struct {
	Proof
	ListingTitle string `json:"listing_title"`
	AddressLine1 string `json:"address_line1"`
	AddressLine2 string `json:"address_line2,omitempty"`
	City         string `json:"city"`
	Region       string `json:"region,omitempty"`
	PostalCode   string `json:"postal_code,omitempty"`
	CountryCode  string `json:"country_code"`
	IDFirstName  string `json:"id_first_name,omitempty"`
	IDMiddleName string `json:"id_middle_name,omitempty"`
	IDLastName   string `json:"id_last_name,omitempty"`
	IDNameSuffix string `json:"id_name_suffix,omitempty"`
	DocumentURL  string `json:"document_url,omitempty"`
}

type UploadTicket struct {
	UploadURL   string `json:"upload_url"`
	ObjectKey   string `json:"object_key"`
	ContentType string `json:"content_type"`
	ExpiresIn   int    `json:"expires_in_seconds"`
	MaxBytes    int64  `json:"max_bytes"`
}

type ObjectInfo struct {
	Size        int64
	ContentType string
}

type ObjectStore interface {
	PresignUpload(ctx context.Context, key, contentType string, expires time.Duration) (string, error)
	PresignDownload(ctx context.Context, key string, expires time.Duration) (string, error)
	Stat(ctx context.Context, key string) (ObjectInfo, error)
	Remove(ctx context.Context, key string) error
}

type Store interface {
	ListingHost(ctx context.Context, actingID, listingID uuid.UUID) (uuid.UUID, error)
	Save(ctx context.Context, proof Proof) error
	Latest(ctx context.Context, actingID, listingID uuid.UUID) (Proof, bool, error)
	IsAdmin(ctx context.Context, accountID uuid.UUID) (bool, error)
	ListSubmitted(ctx context.Context, adminID uuid.UUID) ([]ReviewItem, error)
	Decide(ctx context.Context, adminID, proofID uuid.UUID, status Status, reason string, at time.Time) error
}

type UploadRequester interface {
	RequestUpload(ctx context.Context, hostID, listingID uuid.UUID, contentType string, sizeBytes int64) (UploadTicket, error)
}

type Submitter interface {
	Submit(ctx context.Context, hostID, listingID uuid.UUID, documentType DocumentType, objectKey string) (Proof, error)
}

type StatusReader interface {
	Status(ctx context.Context, hostID, listingID uuid.UUID) (Proof, bool, error)
}

type AdminChecker interface {
	IsAdmin(ctx context.Context, accountID uuid.UUID) (bool, error)
}

type ReviewQueue interface {
	ListSubmitted(ctx context.Context, adminID uuid.UUID) ([]ReviewItem, error)
	Decide(ctx context.Context, adminID, proofID uuid.UUID, decision Decision, reason string) error
}

type IDName struct {
	First  string
	Last   string
	Suffix string
}

type PropertyAddress struct {
	Line1       string
	Line2       string
	City        string
	PostalCode  string
	CountryCode string
}

type CheckSubject struct {
	Address    PropertyAddress
	AddressKey *string
	UnitKey    *string
}

type CheckInquiry struct {
	ID           uuid.UUID
	InquiryID    string
	ListingID    uuid.UUID
	HostID       uuid.UUID
	DocumentType DocumentType
	AddressKey   *string
	UnitKey      *string
	CreatedAt    time.Time
}

type CheckTicket struct {
	InquiryID    string `json:"inquiry_id"`
	SessionToken string `json:"session_token"`
}

type CheckEvent struct {
	AccountID uuid.UUID
	InquiryID string
	Name      string
	Body      []byte
}

type CheckStore interface {
	ListingHost(ctx context.Context, actingID, listingID uuid.UUID) (uuid.UUID, error)
	Latest(ctx context.Context, actingID, listingID uuid.UUID) (Proof, bool, error)
	CheckSubject(ctx context.Context, hostID, listingID uuid.UUID) (CheckSubject, error)
	IDName(ctx context.Context, hostID uuid.UUID) (IDName, bool, error)
	SaveInquiry(ctx context.Context, inquiry CheckInquiry) error
	FindInquiry(ctx context.Context, hostID uuid.UUID, inquiryID string) (CheckInquiry, bool, error)
	HasProofForInquiry(ctx context.Context, hostID uuid.UUID, inquiryID string) (bool, error)
	SaveFromInquiry(ctx context.Context, proof Proof) error
	ApproveFromInquiry(ctx context.Context, hostID uuid.UUID, inquiryID string, at time.Time) (bool, error)
}

type InquiryStarter interface {
	StartInquiry(ctx context.Context, referenceID string, fields map[string]string) (CheckTicket, error)
}

type IDChecker interface {
	HasCurrentID(ctx context.Context, accountID uuid.UUID) (bool, error)
}

type CheckStarter interface {
	Start(ctx context.Context, hostID, listingID uuid.UUID, documentType DocumentType) (CheckTicket, error)
}
