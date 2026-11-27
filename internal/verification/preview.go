package verification

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/BetterGuests/BetterGuests-Server/db"
	"github.com/BetterGuests/BetterGuests-Server/internal/accounts"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const (
	selfieType      = "verification/selfie"
	personaFileHost = "files.withpersona.com"
)

type ReviewRole string

const (
	RoleThis         ReviewRole = "this"
	RolePrevious     ReviewRole = "previous"
	RoleOtherAccount ReviewRole = "other_account"
)

type ReviewInquiry struct {
	Role      ReviewRole
	InquiryID string
}

type ReviewedID struct {
	Role           ReviewRole `json:"role"`
	Available      bool       `json:"available"`
	Name           string     `json:"name"`
	BirthDate      string     `json:"birth_date"`
	IDClass        string     `json:"id_class"`
	CountryCode    string     `json:"country_code"`
	ExpiresOn      string     `json:"expires_on"`
	FrontPhotoURL  string     `json:"front_photo_url"`
	BackPhotoURL   string     `json:"back_photo_url"`
	SelfiePhotoURL string     `json:"selfie_photo_url"`
}

type ReviewInquiryStore interface {
	ReviewInquiries(ctx context.Context, adminID, verificationID uuid.UUID) ([]ReviewInquiry, error)
}

func (service *ReviewService) WithInquiries(store ReviewInquiryStore, reader InquiryReader) *ReviewService {
	service.inquiries = store
	service.reader = reader
	return service
}

func (service *ReviewService) Preview(ctx context.Context, adminID, verificationID uuid.UUID) ([]ReviewedID, error) {
	if service.inquiries == nil || service.reader == nil {
		return nil, ErrPreviewUnavailable
	}
	inquiries, err := service.inquiries.ReviewInquiries(ctx, adminID, verificationID)
	if err != nil {
		return nil, err
	}
	reviewed := make([]ReviewedID, 0, len(inquiries))
	for _, inquiry := range inquiries {
		snapshot, err := service.reader.GetInquiry(ctx, inquiry.InquiryID)
		if err != nil {
			slog.Warn("ID preview could not read Persona inquiry", "inquiry_id", inquiry.InquiryID, "error", err)
			reviewed = append(reviewed, ReviewedID{Role: inquiry.Role})
			continue
		}
		reviewed = append(reviewed, reviewedIDFrom(inquiry.Role, snapshot.Included))
	}
	return reviewed, nil
}

var ErrPreviewUnavailable = errors.New("ID previews are not available")

type previewGovernmentID struct {
	Status         string `json:"status"`
	First          string `json:"name-first"`
	Middle         string `json:"name-middle"`
	Last           string `json:"name-last"`
	Suffix         string `json:"name-suffix"`
	Born           string `json:"birthdate"`
	IDClass        string `json:"id-class"`
	CountryCode    string `json:"country-code"`
	ExpirationDate string `json:"expiration-date"`
	FrontPhotoURL  string `json:"front-photo-url"`
	BackPhotoURL   string `json:"back-photo-url"`
	SelfiePhotoURL string `json:"selfie-photo-url"`
}

type previewSelfie struct {
	Status         string `json:"status"`
	CenterPhotoURL string `json:"center-photo-url"`
}

func reviewedIDFrom(role ReviewRole, included []includedObject) ReviewedID {
	reviewed := ReviewedID{Role: role}
	var governmentID *previewGovernmentID
	selfieURL := ""
	for _, object := range included {
		switch object.Type {
		case governmentIDType:
			var attributes previewGovernmentID
			if json.Unmarshal(object.Attributes, &attributes) != nil {
				continue
			}
			if governmentID == nil || governmentID.Status != passedCheckStatus || attributes.Status == passedCheckStatus {
				governmentID = &attributes
			}
		case selfieType:
			var attributes previewSelfie
			if json.Unmarshal(object.Attributes, &attributes) == nil && personaFileURL(attributes.CenterPhotoURL) != "" &&
				(selfieURL == "" || attributes.Status == passedCheckStatus) {
				selfieURL = personaFileURL(attributes.CenterPhotoURL)
			}
		}
	}
	if governmentID == nil {
		return reviewed
	}
	reviewed.Available = true
	reviewed.Name = joinNameParts(governmentID.First, governmentID.Middle, governmentID.Last, governmentID.Suffix)
	reviewed.BirthDate = strings.TrimSpace(governmentID.Born)
	reviewed.IDClass = strings.TrimSpace(governmentID.IDClass)
	reviewed.CountryCode = strings.TrimSpace(governmentID.CountryCode)
	reviewed.ExpiresOn = strings.TrimSpace(governmentID.ExpirationDate)
	reviewed.FrontPhotoURL = personaFileURL(governmentID.FrontPhotoURL)
	reviewed.BackPhotoURL = personaFileURL(governmentID.BackPhotoURL)
	reviewed.SelfiePhotoURL = selfieURL
	if reviewed.SelfiePhotoURL == "" {
		reviewed.SelfiePhotoURL = personaFileURL(governmentID.SelfiePhotoURL)
	}
	return reviewed
}

func joinNameParts(parts ...string) string {
	var kept []string
	for _, part := range parts {
		if trimmed := namePart(part); trimmed != "" {
			kept = append(kept, trimmed)
		}
	}
	return strings.Join(kept, " ")
}

func personaFileURL(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "https" || parsed.Host != personaFileHost || parsed.User != nil {
		return ""
	}
	return parsed.String()
}

func (store *PostgresVerificationStore) ReviewInquiries(ctx context.Context, adminID, verificationID uuid.UUID) ([]ReviewInquiry, error) {
	var inquiries []ReviewInquiry
	err := db.WithUserTx(ctx, store.DB, adminID.String(), func(tx pgx.Tx) error {
		var admin bool
		if err := tx.QueryRow(ctx, `SELECT app.is_admin()`).Scan(&admin); err != nil {
			return fmt.Errorf("check admin: %w", err)
		}
		if !admin {
			return ErrNotAdmin
		}
		rows, err := tx.Query(ctx, `SELECT role, persona_inquiry_id FROM app.identity_review_inquiries($1)`, verificationID)
		if err != nil {
			return fmt.Errorf("list ID review inquiries: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var inquiry ReviewInquiry
			var role string
			if err := rows.Scan(&role, &inquiry.InquiryID); err != nil {
				return fmt.Errorf("scan ID review inquiry: %w", err)
			}
			inquiry.Role = ReviewRole(role)
			inquiries = append(inquiries, inquiry)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	if len(inquiries) == 0 {
		return nil, ErrReviewNotFound
	}
	return inquiries, nil
}

func (handler *HTTPHandler) PreviewReview(w http.ResponseWriter, r *http.Request) {
	adminID, ok := accounts.UserID(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	verificationID, err := uuid.Parse(r.PathValue("verification_id"))
	if handler.reviews == nil || err != nil {
		writeError(w, http.StatusNotFound, "verification not found")
		return
	}
	reviewed, err := handler.reviews.Preview(r.Context(), adminID, verificationID)
	if errors.Is(err, ErrPreviewUnavailable) {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	if err != nil {
		writeReviewError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]any{"ids": reviewed})
}
