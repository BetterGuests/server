package proofs

import (
	"errors"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

var (
	ErrInvalidRelationship = errors.New("choose how you are connected to this home")
	ErrWrongDocument       = errors.New("that document does not work for your connection to this home")
	ErrAttachments         = errors.New("add each required document once")
	ErrOwnerDetails        = errors.New("add the owner's name and an email or phone number")
	ErrExpiry              = errors.New("add the date your permission ends, in the future")
	ErrAuthorityExpired    = errors.New("the permission on this proof has already ended")
)

const (
	maxOwnerNameLength    = 200
	minOwnerContactLength = 3
	maxOwnerContactLength = 254
)

var ownershipDocuments = []DocumentType{Deed, PropertyTaxBill, UtilityBill}

var authorityRules = map[Relationship]authorityRule{
	Owner: {documents: ownershipDocuments},
	CompanyOwner: {
		documents: ownershipDocuments,
		required:  []AttachmentKind{CompanyRegistration},
		optional:  []AttachmentKind{SigningAuthorization},
	},
	PropertyManager: {documents: ownershipDocuments, required: []AttachmentKind{ManagementAgreement}, needsOwner: true},
	Renter:          {documents: []DocumentType{Lease}, required: []AttachmentKind{LandlordPermission}, needsOwner: true},
}

func checkedSubmission(submission Submission, today time.Time) (Submission, error) {
	if submission.Relationship == "" {
		submission.Relationship = Owner
	}
	rule, known := authorityRules[submission.Relationship]
	if !known {
		return Submission{}, ErrInvalidRelationship
	}
	if !documentTypes[submission.DocumentType] {
		return Submission{}, ErrInvalidDocument
	}
	if !slices.Contains(rule.documents, submission.DocumentType) {
		return Submission{}, ErrWrongDocument
	}
	if !attachmentsSatisfy(submission.Attachments, rule) {
		return Submission{}, ErrAttachments
	}
	if !rule.needsOwner {
		submission.OwnerName, submission.OwnerContact, submission.ExpiresOn = "", "", ""
		return submission, nil
	}
	submission.OwnerName = strings.TrimSpace(submission.OwnerName)
	submission.OwnerContact = strings.TrimSpace(submission.OwnerContact)
	nameLength, contactLength := utf8.RuneCountInString(submission.OwnerName), utf8.RuneCountInString(submission.OwnerContact)
	if nameLength == 0 || nameLength > maxOwnerNameLength ||
		contactLength < minOwnerContactLength || contactLength > maxOwnerContactLength {
		return Submission{}, ErrOwnerDetails
	}
	if expired(submission.ExpiresOn, today) {
		return Submission{}, ErrExpiry
	}
	return submission, nil
}

func attachmentsSatisfy(attachments []SubmittedAttachment, rule authorityRule) bool {
	seen := map[AttachmentKind]bool{}
	keys := map[string]bool{}
	for _, attachment := range attachments {
		allowed := slices.Contains(rule.required, attachment.Kind) || slices.Contains(rule.optional, attachment.Kind)
		if !allowed || seen[attachment.Kind] || keys[attachment.ObjectKey] {
			return false
		}
		seen[attachment.Kind], keys[attachment.ObjectKey] = true, true
	}
	for _, kind := range rule.required {
		if !seen[kind] {
			return false
		}
	}
	return true
}

func expired(date string, today time.Time) bool {
	ends, err := time.Parse(time.DateOnly, date)
	return err != nil || ends.Format(time.DateOnly) < today.UTC().Format(time.DateOnly)
}
