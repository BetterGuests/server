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
	Relationship    Relationship `json:"relationship"`
	OwnerName       string       `json:"owner_name,omitempty"`
	OwnerContact    string       `json:"owner_contact,omitempty"`
	ExpiresOn       string       `json:"authority_expires_on,omitempty"`
	Attachments     []Attachment `json:"attachments,omitempty"`
	ContentType     string       `json:"content_type"`
	SizeBytes       int64        `json:"size_bytes"`
	Status          Status       `json:"status"`
	RejectionReason string       `json:"rejection_reason,omitempty"`
	ReviewedBy      *uuid.UUID   `json:"reviewed_by,omitempty"`
	ReviewedAt      *time.Time   `json:"reviewed_at,omitempty"`
	CreatedAt       time.Time    `json:"created_at"`
	UpdatedAt       time.Time    `json:"updated_at"`
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
	HostName     string `json:"host_name,omitempty"`
	CoverKey     string `json:"-"`
	CoverURL     string `json:"cover_photo_url,omitempty"`
	OwnListing   bool   `json:"own_listing"`
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
	ListApproved(ctx context.Context, adminID uuid.UUID) ([]ReviewItem, error)
	Revoke(ctx context.Context, adminID, proofID uuid.UUID, reason string, at time.Time) error
}

type UploadRequester interface {
	RequestUpload(ctx context.Context, hostID, listingID uuid.UUID, contentType string, sizeBytes int64) (UploadTicket, error)
}

type Submitter interface {
	SubmitAuthority(ctx context.Context, hostID, listingID uuid.UUID, submission Submission) (Proof, error)
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
	ListApproved(ctx context.Context, adminID uuid.UUID) ([]ReviewItem, error)
	Revoke(ctx context.Context, adminID, proofID uuid.UUID, reason string) error
}

type DueFile struct {
	ProofID   uuid.UUID
	ObjectKey string
}

type FileRetentionStore interface {
	FilesDueForDeletion(ctx context.Context, batchSize int) ([]DueFile, error)
	MarkFileDeleted(ctx context.Context, proofID uuid.UUID) error
	AuthorityStore
}

type FileRemover interface {
	Remove(ctx context.Context, key string) error
}

type Relationship string

const (
	Owner           Relationship = "owner"
	CompanyOwner    Relationship = "company_owner"
	PropertyManager Relationship = "property_manager"
	Renter          Relationship = "renter"
)

type AttachmentKind string

const (
	CompanyRegistration  AttachmentKind = "company_registration"
	SigningAuthorization AttachmentKind = "signing_authorization"
	ManagementAgreement  AttachmentKind = "management_agreement"
	LandlordPermission   AttachmentKind = "landlord_permission"
)

type Attachment struct {
	ID          uuid.UUID      `json:"-"`
	Kind        AttachmentKind `json:"kind"`
	ContentType string         `json:"-"`
	SizeBytes   int64          `json:"-"`
	ObjectKey   string         `json:"-"`
	URL         string         `json:"url,omitempty"`
}

type SubmittedAttachment struct {
	Kind      AttachmentKind `json:"kind"`
	ObjectKey string         `json:"object_key"`
}

type Submission struct {
	Relationship Relationship          `json:"relationship"`
	DocumentType DocumentType          `json:"document_type"`
	ObjectKey    string                `json:"object_key"`
	Attachments  []SubmittedAttachment `json:"attachments"`
	OwnerName    string                `json:"owner_name"`
	OwnerContact string                `json:"owner_contact"`
	ExpiresOn    string                `json:"authority_expires_on"`
}

type authorityRule struct {
	documents  []DocumentType
	required   []AttachmentKind
	optional   []AttachmentKind
	needsOwner bool
}

type AuthorityStore interface {
	ExpireAuthority(ctx context.Context, today time.Time) (int, error)
}
