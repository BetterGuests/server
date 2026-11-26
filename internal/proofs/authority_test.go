package proofs

import (
	"errors"
	"testing"
	"time"
)

func TestCheckedSubmission(t *testing.T) {
	today := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	managed := Submission{
		Relationship: PropertyManager, DocumentType: Deed, ObjectKey: "a",
		Attachments: []SubmittedAttachment{{Kind: ManagementAgreement, ObjectKey: "b"}},
		OwnerName:   "Sam Owner", OwnerContact: "sam@example.test", ExpiresOn: "2027-11-25",
	}
	cases := []struct {
		name    string
		change  func(*Submission)
		wantErr error
	}{
		{"a complete manager submission", func(*Submission) {}, nil},
		{"permission ending today still counts", func(s *Submission) { s.ExpiresOn = "2026-11-25" }, nil},
		{"unknown connection", func(s *Submission) { s.Relationship = "friend" }, ErrInvalidRelationship},
		{"a manager cannot use a lease", func(s *Submission) { s.DocumentType = Lease }, ErrWrongDocument},
		{"missing agreement", func(s *Submission) { s.Attachments = nil }, ErrAttachments},
		{"someone else's paperwork", func(s *Submission) { s.Attachments[0].Kind = LandlordPermission }, ErrAttachments},
		{"missing owner", func(s *Submission) { s.OwnerName = " " }, ErrOwnerDetails},
		{"missing contact", func(s *Submission) { s.OwnerContact = "" }, ErrOwnerDetails},
		{"ended permission", func(s *Submission) { s.ExpiresOn = "2026-11-24" }, ErrExpiry},
		{"no end date", func(s *Submission) { s.ExpiresOn = "" }, ErrExpiry},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			submission := managed
			submission.Attachments = append([]SubmittedAttachment{}, managed.Attachments...)
			testCase.change(&submission)

			_, err := checkedSubmission(submission, today)

			if !errors.Is(err, testCase.wantErr) {
				t.Fatalf("expected %v, got %v", testCase.wantErr, err)
			}
		})
	}
}

func TestAnOwnerSubmissionKeepsNoThirdPartyDetails(t *testing.T) {
	checked, err := checkedSubmission(Submission{DocumentType: Deed, OwnerName: "Someone", OwnerContact: "x@example.test"}, time.Now())

	if err != nil || checked.Relationship != Owner || checked.OwnerName != "" || checked.OwnerContact != "" {
		t.Fatalf("%+v %v", checked, err)
	}
}
