package proofs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
)

var (
	ErrChecksDisabled = errors.New("automatic document checks are not available")
	ErrIDRequired     = errors.New("verify your ID before checking a document")
	ErrAlreadyProven  = errors.New("this listing already has an approved proof")
)

const maxDocumentFiles = 10

var approvingEvents = map[string]bool{"inquiry.completed": true, "inquiry.approved": true}

var reviewableEvents = map[string]bool{
	"inquiry.completed": true, "inquiry.approved": true, "inquiry.failed": true,
	"inquiry.declined": true, "inquiry.marked-for-review": true,
}

type CheckService struct {
	store     CheckStore
	inquiries InquiryStarter
	ids       IDChecker
	objects   ObjectStore
	files     FileFetcher
	now       func() time.Time
	newID     func() uuid.UUID
}

func NewCheckService(store CheckStore, inquiries InquiryStarter, ids IDChecker, objects ObjectStore, files FileFetcher) *CheckService {
	return &CheckService{store: store, inquiries: inquiries, ids: ids, objects: objects, files: files, now: time.Now, newID: uuid.New}
}

func (service *CheckService) Start(ctx context.Context, hostID, listingID uuid.UUID, documentType DocumentType) (CheckTicket, error) {
	if service.inquiries == nil || service.objects == nil {
		return CheckTicket{}, ErrChecksDisabled
	}
	if !documentTypes[documentType] {
		return CheckTicket{}, ErrInvalidDocument
	}
	owner, err := service.store.ListingHost(ctx, hostID, listingID)
	if err != nil {
		return CheckTicket{}, err
	}
	if owner != hostID {
		return CheckTicket{}, ErrNotListingOwner
	}
	verified, err := service.ids.HasCurrentID(ctx, hostID)
	if err != nil {
		return CheckTicket{}, fmt.Errorf("check host ID: %w", err)
	}
	name, named, err := service.store.IDName(ctx, hostID)
	if err != nil {
		return CheckTicket{}, err
	}
	if !verified || !named {
		return CheckTicket{}, ErrIDRequired
	}
	subject, err := service.store.CheckSubject(ctx, hostID, listingID)
	if err != nil {
		return CheckTicket{}, err
	}
	if err := service.requireNoOpenProof(ctx, hostID, listingID, subject); err != nil {
		return CheckTicket{}, err
	}
	ticket, err := service.inquiries.StartInquiry(ctx, hostID.String(), prefill(name, subject.Address))
	if err != nil {
		return CheckTicket{}, fmt.Errorf("start document inquiry: %w", err)
	}
	err = service.store.SaveInquiry(ctx, CheckInquiry{
		ID: service.newID(), InquiryID: ticket.InquiryID, ListingID: listingID, HostID: hostID,
		DocumentType: documentType, AddressKey: subject.AddressKey, UnitKey: subject.UnitKey,
		CreatedAt: service.now().UTC(),
	})
	if err != nil {
		return CheckTicket{}, err
	}
	return ticket, nil
}

func (service *CheckService) requireNoOpenProof(ctx context.Context, hostID, listingID uuid.UUID, subject CheckSubject) error {
	latest, found, err := service.store.Latest(ctx, hostID, listingID)
	if err != nil || !found {
		return err
	}
	switch {
	case latest.Status == Submitted:
		return ErrProofPending
	case latest.Status == Approved && sameKey(latest.AddressKey, subject.AddressKey) && sameKey(latest.UnitKey, subject.UnitKey):
		return ErrAlreadyProven
	}
	return nil
}

func sameKey(left, right *string) bool {
	if left == nil || right == nil {
		return left == right
	}
	return *left == *right
}

func prefill(name IDName, address PropertyAddress) map[string]string {
	fields := map[string]string{}
	for key, value := range map[string]string{
		"name-first":           name.First,
		"name-last":            name.Last,
		"address-street-1":     address.Line1,
		"address-street-2":     address.Line2,
		"address-city":         address.City,
		"address-postal-code":  address.PostalCode,
		"address-country-code": address.CountryCode,
	} {
		if value != "" {
			fields[key] = value
		}
	}
	return fields
}

func (service *CheckService) Process(ctx context.Context, event CheckEvent) (bool, error) {
	inquiry, found, err := service.store.FindInquiry(ctx, event.AccountID, event.InquiryID)
	if err != nil || !found {
		return false, err
	}
	if !reviewableEvents[event.Name] {
		return true, nil
	}
	if service.objects == nil {
		return true, ErrUploadsDisabled
	}
	document, present, err := chosenDocument(event.Body)
	if err != nil {
		return true, err
	}
	if !present {
		slog.Warn("persona document event has no files; check that the webhook includes related objects",
			"event", event.Name, "inquiry_id", event.InquiryID)
		return true, nil
	}
	saved, err := service.store.HasProofForInquiry(ctx, inquiry.HostID, inquiry.InquiryID)
	if err != nil {
		return true, err
	}
	if !saved {
		if err := service.keep(ctx, inquiry, document); err != nil {
			return true, ignoreUnkeepable(err, inquiry.InquiryID)
		}
	}
	if !approvingEvents[event.Name] {
		return true, nil
	}
	return true, service.approveIfMatching(ctx, inquiry, document)
}

func ignoreUnkeepable(err error, inquiryID string) error {
	if errors.Is(err, ErrDocumentRejected) || errors.Is(err, ErrProofPending) || errors.Is(err, ErrListingNotFound) {
		slog.Warn("persona document was not kept", "inquiry_id", inquiryID, "error", err)
		return nil
	}
	return err
}

func (service *CheckService) keep(ctx context.Context, inquiry CheckInquiry, document documentResult) error {
	if len(document.Files) > maxDocumentFiles {
		return ErrDocumentRejected
	}
	now := service.now().UTC()
	proof := Proof{
		ID: service.newID(), ListingID: inquiry.ListingID, HostID: inquiry.HostID,
		DocumentType: inquiry.DocumentType, Status: Submitted, CreatedAt: now, UpdatedAt: now,
		InquiryID: inquiry.InquiryID, AddressKey: inquiry.AddressKey, UnitKey: inquiry.UnitKey,
	}
	var written []string
	for index, file := range document.Files {
		data, err := service.files.Fetch(ctx, file.URL, maxDocumentBytes)
		if err != nil {
			service.discardAll(ctx, written)
			return fmt.Errorf("download persona document: %w", err)
		}
		contentType, extension, allowed := documentKind(file.Filename, data)
		if !allowed || len(data) == 0 {
			service.discardAll(ctx, written)
			return ErrDocumentRejected
		}
		key := keyPrefix(inquiry.ListingID) + service.newID().String() + extension
		if err := service.objects.Write(ctx, key, contentType, data); err != nil {
			service.discardAll(ctx, written)
			return fmt.Errorf("store persona document: %w", err)
		}
		written = append(written, key)
		if index == 0 {
			proof.ObjectKey, proof.ContentType, proof.SizeBytes = key, contentType, int64(len(data))
		}
	}
	proof.ExtraObjectKeys = written[1:]
	if err := service.store.SaveFromInquiry(ctx, proof); err != nil {
		service.discardAll(ctx, written)
		return err
	}
	return nil
}

func (service *CheckService) discardAll(ctx context.Context, keys []string) {
	for _, key := range keys {
		if err := service.objects.Remove(ctx, key); err != nil {
			slog.Warn("could not remove unkept document", "error", err)
		}
	}
}

func documentKind(filename string, data []byte) (string, string, bool) {
	extension := strings.ToLower(filepath.Ext(filename))
	if extension == ".jpeg" {
		extension = ".jpg"
	}
	sniffed := http.DetectContentType(data)
	if _, allowed := allowedContentTypes[sniffed]; allowed {
		return sniffed, allowedContentTypes[sniffed], true
	}
	if extension == ".heic" && sniffed == "application/octet-stream" {
		return "image/heic", extension, true
	}
	return "", "", false
}

func (service *CheckService) approveIfMatching(ctx context.Context, inquiry CheckInquiry, document documentResult) error {
	if !document.clean() {
		return service.leaveForReview(inquiry, "persona checks did not all pass")
	}
	name, named, err := service.store.IDName(ctx, inquiry.HostID)
	if err != nil {
		return err
	}
	subject, err := service.store.CheckSubject(ctx, inquiry.HostID, inquiry.ListingID)
	if err != nil {
		return err
	}
	documentName, hasName := document.extracted(ownerNameField)
	documentAddress, hasAddress := document.extracted(propertyAddressField)
	switch {
	case !named || !hasName || !nameMatches(documentName, name):
		return service.leaveForReview(inquiry, "name could not be matched")
	case !hasAddress || !addressMatches(documentAddress, subject.Address):
		return service.leaveForReview(inquiry, "address could not be matched")
	}
	approved, err := service.store.ApproveFromInquiry(ctx, inquiry.HostID, inquiry.InquiryID, service.now().UTC())
	if err != nil {
		return err
	}
	if approved {
		slog.Info("property proof approved automatically", "inquiry_id", inquiry.InquiryID, "listing_id", inquiry.ListingID)
	}
	return nil
}

func (service *CheckService) leaveForReview(inquiry CheckInquiry, reason string) error {
	slog.Info("property proof left for manual review", "inquiry_id", inquiry.InquiryID, "reason", reason)
	return nil
}
