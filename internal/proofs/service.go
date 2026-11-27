package proofs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

var (
	ErrUploadsDisabled   = errors.New("document uploads are not available")
	ErrDocumentRejected  = errors.New("document must be a PDF or image up to 15 MB")
	ErrInvalidDocument   = errors.New("unknown document type")
	ErrInvalidObjectKey  = errors.New("invalid document key")
	ErrNotUploaded       = errors.New("document has not been uploaded")
	ErrListingNotFound   = errors.New("listing not found")
	ErrNotListingOwner   = errors.New("only the host can submit proof for this listing")
	ErrProofPending      = errors.New("a proof is already waiting for review")
	ErrNotAdmin          = errors.New("admin access required")
	ErrProofNotFound     = errors.New("proof not found")
	ErrNotAwaitingReview = errors.New("proof is not awaiting review")
	ErrInvalidDecision   = errors.New("decision must be approve or reject")
	ErrReasonRequired    = errors.New("a rejection needs a reason of 500 characters or fewer")
	ErrNotApproved       = errors.New("proof is not approved")
)

const (
	maxDocumentBytes    = 15 << 20
	maxReasonLength     = 500
	uploadURLLifetime   = 5 * time.Minute
	downloadURLLifetime = 5 * time.Minute
)

var allowedContentTypes = map[string]string{
	"application/pdf": ".pdf",
	"image/jpeg":      ".jpg",
	"image/png":       ".png",
	"image/webp":      ".webp",
	"image/heic":      ".heic",
}

var documentTypes = map[DocumentType]bool{
	UtilityBill: true, Deed: true, Lease: true, PropertyTaxBill: true,
}

var objectKeyPattern = regexp.MustCompile(`^[0-9a-f-]{36}\.(pdf|jpg|png|webp|heic)$`)

func keyPrefix(listingID uuid.UUID) string {
	return "property-proofs/" + listingID.String() + "/"
}

func validObjectKey(key string, listingID uuid.UUID) bool {
	prefix := keyPrefix(listingID)
	return strings.HasPrefix(key, prefix) && objectKeyPattern.MatchString(strings.TrimPrefix(key, prefix))
}

type ProofService struct {
	store   Store
	objects ObjectStore
	now     func() time.Time
	newID   func() uuid.UUID
}

func NewProofService(store Store, objects ObjectStore) *ProofService {
	return &ProofService{store: store, objects: objects, now: time.Now, newID: uuid.New}
}

func (service *ProofService) requireOwner(ctx context.Context, hostID, listingID uuid.UUID) error {
	owner, err := service.store.ListingHost(ctx, hostID, listingID)
	if err != nil {
		return err
	}
	if owner != hostID {
		return ErrNotListingOwner
	}
	return nil
}

func (service *ProofService) RequestUpload(ctx context.Context, hostID, listingID uuid.UUID, contentType string, sizeBytes int64) (UploadTicket, error) {
	if service.objects == nil {
		return UploadTicket{}, ErrUploadsDisabled
	}
	extension, allowed := allowedContentTypes[contentType]
	if !allowed || sizeBytes <= 0 || sizeBytes > maxDocumentBytes {
		return UploadTicket{}, ErrDocumentRejected
	}
	if err := service.requireOwner(ctx, hostID, listingID); err != nil {
		return UploadTicket{}, err
	}
	key := keyPrefix(listingID) + service.newID().String() + extension
	url, err := service.objects.PresignUpload(ctx, key, contentType, uploadURLLifetime)
	if err != nil {
		return UploadTicket{}, fmt.Errorf("presign upload: %w", err)
	}
	return UploadTicket{
		UploadURL:   url,
		ObjectKey:   key,
		ContentType: contentType,
		ExpiresIn:   int(uploadURLLifetime.Seconds()),
		MaxBytes:    maxDocumentBytes,
	}, nil
}

func (service *ProofService) Submit(ctx context.Context, hostID, listingID uuid.UUID, documentType DocumentType, objectKey string) (Proof, error) {
	return service.SubmitAuthority(ctx, hostID, listingID, Submission{Relationship: Owner, DocumentType: documentType, ObjectKey: objectKey})
}

func (service *ProofService) SubmitAuthority(ctx context.Context, hostID, listingID uuid.UUID, submission Submission) (Proof, error) {
	if service.objects == nil {
		return Proof{}, ErrUploadsDisabled
	}
	if !documentTypes[submission.DocumentType] {
		return Proof{}, ErrInvalidDocument
	}
	if !validObjectKey(submission.ObjectKey, listingID) {
		return Proof{}, ErrInvalidObjectKey
	}
	for _, attachment := range submission.Attachments {
		if !validObjectKey(attachment.ObjectKey, listingID) || attachment.ObjectKey == submission.ObjectKey {
			return Proof{}, ErrInvalidObjectKey
		}
	}
	if err := service.requireOwner(ctx, hostID, listingID); err != nil {
		return Proof{}, err
	}
	now := service.now().UTC()
	submission, err := checkedSubmission(submission, now)
	if err != nil {
		return Proof{}, err
	}
	info, err := service.uploaded(ctx, submission.ObjectKey)
	if err != nil {
		return Proof{}, err
	}
	proof := Proof{
		ID: service.newID(), ListingID: listingID, HostID: hostID, DocumentType: submission.DocumentType,
		Relationship: submission.Relationship, OwnerName: submission.OwnerName,
		OwnerContact: submission.OwnerContact, ExpiresOn: submission.ExpiresOn,
		ContentType: info.ContentType, SizeBytes: info.Size, Status: Submitted,
		CreatedAt: now, UpdatedAt: now, ObjectKey: submission.ObjectKey,
	}
	for _, attachment := range submission.Attachments {
		info, err := service.uploaded(ctx, attachment.ObjectKey)
		if err != nil {
			return Proof{}, err
		}
		proof.Attachments = append(proof.Attachments, Attachment{
			ID: service.newID(), Kind: attachment.Kind, ContentType: info.ContentType,
			SizeBytes: info.Size, ObjectKey: attachment.ObjectKey,
		})
	}
	if err := service.store.Save(ctx, proof); err != nil {
		return Proof{}, err
	}
	return proof, nil
}

func (service *ProofService) uploaded(ctx context.Context, objectKey string) (ObjectInfo, error) {
	info, err := service.objects.Stat(ctx, objectKey)
	if err != nil {
		return ObjectInfo{}, ErrNotUploaded
	}
	if _, allowed := allowedContentTypes[info.ContentType]; !allowed || info.Size <= 0 || info.Size > maxDocumentBytes {
		service.discard(ctx, objectKey)
		return ObjectInfo{}, ErrDocumentRejected
	}
	return info, nil
}

func (service *ProofService) discard(ctx context.Context, key string) {
	if err := service.objects.Remove(ctx, key); err != nil {
		slog.Warn("could not remove rejected document", "error", err)
	}
}

func (service *ProofService) Status(ctx context.Context, hostID, listingID uuid.UUID) (Proof, bool, error) {
	if err := service.requireOwner(ctx, hostID, listingID); err != nil {
		return Proof{}, false, err
	}
	return service.store.Latest(ctx, hostID, listingID)
}

func (service *ProofService) IsAdmin(ctx context.Context, accountID uuid.UUID) (bool, error) {
	return service.store.IsAdmin(ctx, accountID)
}

func (service *ProofService) requireAdmin(ctx context.Context, accountID uuid.UUID) error {
	admin, err := service.store.IsAdmin(ctx, accountID)
	if err != nil {
		return fmt.Errorf("check admin: %w", err)
	}
	if !admin {
		return ErrNotAdmin
	}
	return nil
}

func (service *ProofService) ListSubmitted(ctx context.Context, adminID uuid.UUID) ([]ReviewItem, error) {
	if err := service.requireAdmin(ctx, adminID); err != nil {
		return nil, err
	}
	items, err := service.store.ListSubmitted(ctx, adminID)
	return service.withDocumentLinks(ctx, markOwnListings(items, adminID), err)
}

func (service *ProofService) ListApproved(ctx context.Context, adminID uuid.UUID) ([]ReviewItem, error) {
	if err := service.requireAdmin(ctx, adminID); err != nil {
		return nil, err
	}
	items, err := service.store.ListApproved(ctx, adminID)
	return service.withDocumentLinks(ctx, markOwnListings(items, adminID), err)
}

func (service *ProofService) Revoke(ctx context.Context, adminID, proofID uuid.UUID, reason string) error {
	if err := service.requireAdmin(ctx, adminID); err != nil {
		return err
	}
	reason, err := requiredReason(reason)
	if err != nil {
		return err
	}
	if err := service.store.Revoke(ctx, adminID, proofID, reason, service.now().UTC()); err != nil {
		return err
	}
	slog.Info("property proof revoked", "proof_id", proofID, "admin_id", adminID)
	return nil
}

func requiredReason(reason string) (string, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" || len(reason) > maxReasonLength {
		return "", ErrReasonRequired
	}
	return reason, nil
}

func markOwnListings(items []ReviewItem, adminID uuid.UUID) []ReviewItem {
	for index := range items {
		items[index].OwnListing = items[index].HostID == adminID
	}
	return items
}

func (service *ProofService) withDocumentLinks(ctx context.Context, items []ReviewItem, err error) ([]ReviewItem, error) {
	if err != nil {
		return nil, err
	}
	for index := range items {
		items[index].DocumentURL = service.signedDownload(ctx, items[index].ObjectKey)
		items[index].CoverURL = service.signedDownload(ctx, items[index].CoverKey)
		for position := range items[index].Attachments {
			attachment := &items[index].Attachments[position]
			attachment.URL = service.signedDownload(ctx, attachment.ObjectKey)
		}
	}
	return items, nil
}

func (service *ProofService) signedDownload(ctx context.Context, key string) string {
	if service.objects == nil || key == "" {
		return ""
	}
	url, err := service.objects.PresignDownload(ctx, key, downloadURLLifetime)
	if err != nil {
		slog.Error("presign proof download", "error", err)
		return ""
	}
	return url
}

func (service *ProofService) Decide(ctx context.Context, adminID, proofID uuid.UUID, decision Decision, reason string) error {
	if err := service.requireAdmin(ctx, adminID); err != nil {
		return err
	}
	var status Status
	switch decision {
	case Approve:
		status, reason = Approved, ""
	case Reject:
		trimmed, err := requiredReason(reason)
		if err != nil {
			return err
		}
		status, reason = Rejected, trimmed
	default:
		return ErrInvalidDecision
	}
	if err := service.store.Decide(ctx, adminID, proofID, status, reason, service.now().UTC()); err != nil {
		return err
	}
	slog.Info("property proof decided", "proof_id", proofID, "admin_id", adminID, "status", status)
	return nil
}
