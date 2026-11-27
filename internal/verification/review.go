package verification

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/BetterGuests/BetterGuests-Server/db"
	"github.com/BetterGuests/BetterGuests-Server/internal/accounts"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const (
	maxListedReviews = 200
	maxDecisionBytes = 1 << 10
	maxOverturnBytes = 4 << 10
	maxOverturnNote  = 1000
	statusRejected   = "rejected"
	decisionApprove  = "approve"
	decisionReject   = "reject"
	outcomeDone      = "done"
)

var (
	ErrNotAdmin          = errors.New("admin access required")
	ErrReviewNotFound    = errors.New("verification not found")
	ErrOwnVerification   = errors.New("admins cannot review their own ID check")
	ErrNotAwaitingReview = errors.New("verification is not awaiting review")
	ErrInvalidDecision   = errors.New("decision must be approve or reject")
	ErrNoteRequired      = errors.New("write why you are approving it, in up to 1000 characters")
	ErrIDErased          = errors.New("this ID was erased, so it cannot be approved")
	ErrSuperseded        = errors.New("this person has a newer ID check")
)

var reviewOutcomes = map[string]error{
	outcomeDone:   nil,
	"forbidden":   ErrNotAdmin,
	"not_found":   ErrReviewNotFound,
	"own":         ErrOwnVerification,
	"wrong_state": ErrNotAwaitingReview,
	"erased":      ErrIDErased,
	"superseded":  ErrSuperseded,
}

type PendingReview struct {
	VerificationID uuid.UUID      `json:"verification_id"`
	AccountID      uuid.UUID      `json:"account_id"`
	InquiryID      string         `json:"persona_inquiry_id"`
	Reasons        []ReviewReason `json:"reasons"`
	CompletedAt    time.Time      `json:"completed_at"`
	NewName        string         `json:"new_name"`
	PreviousName   string         `json:"previous_name"`
}

type RejectedReview struct {
	PendingReview
	RejectedAt time.Time `json:"rejected_at"`
	Automatic  bool      `json:"automatic"`
	Erased     bool      `json:"erased"`
}

type ReviewStore interface {
	PendingReviews(ctx context.Context, adminID uuid.UUID, limit int) ([]PendingReview, error)
	RejectedReviews(ctx context.Context, adminID uuid.UUID, limit int) ([]RejectedReview, error)
	DecideReview(ctx context.Context, adminID, verificationID uuid.UUID, approve bool, at time.Time) error
	OverturnReview(ctx context.Context, adminID, verificationID uuid.UUID, note string, at time.Time) error
	EraseRejected(ctx context.Context, adminID, verificationID uuid.UUID, at time.Time) error
}

type IdentityReviewer interface {
	Pending(ctx context.Context, adminID uuid.UUID) ([]PendingReview, error)
	Rejected(ctx context.Context, adminID uuid.UUID) ([]RejectedReview, error)
	Decide(ctx context.Context, adminID, verificationID uuid.UUID, decision string) error
	Overturn(ctx context.Context, adminID, verificationID uuid.UUID, note string) error
	Erase(ctx context.Context, adminID, verificationID uuid.UUID) error
	Preview(ctx context.Context, adminID, verificationID uuid.UUID) ([]ReviewedID, error)
}

type ReviewService struct {
	store     ReviewStore
	inquiries ReviewInquiryStore
	reader    InquiryReader
	now       func() time.Time
}

func NewReviewService(store ReviewStore) *ReviewService {
	return &ReviewService{store: store, now: time.Now}
}

func (service *ReviewService) Pending(ctx context.Context, adminID uuid.UUID) ([]PendingReview, error) {
	return service.store.PendingReviews(ctx, adminID, maxListedReviews)
}

func (service *ReviewService) Rejected(ctx context.Context, adminID uuid.UUID) ([]RejectedReview, error) {
	return service.store.RejectedReviews(ctx, adminID, maxListedReviews)
}

func requiredOverturnNote(note string) (string, error) {
	trimmed := strings.TrimSpace(note)
	if trimmed == "" || utf8.RuneCountInString(trimmed) > maxOverturnNote {
		return "", ErrNoteRequired
	}
	return trimmed, nil
}

func (service *ReviewService) Overturn(ctx context.Context, adminID, verificationID uuid.UUID, note string) error {
	trimmed, err := requiredOverturnNote(note)
	if err != nil {
		return err
	}
	if err := service.store.OverturnReview(ctx, adminID, verificationID, trimmed, service.now().UTC()); err != nil {
		return err
	}
	slog.Info("rejected ID check approved", "verification_id", verificationID, "admin_id", adminID)
	return nil
}

func (service *ReviewService) Erase(ctx context.Context, adminID, verificationID uuid.UUID) error {
	if err := service.store.EraseRejected(ctx, adminID, verificationID, service.now().UTC()); err != nil {
		return err
	}
	slog.Info("rejected ID check erased", "verification_id", verificationID, "admin_id", adminID)
	return nil
}

func (service *ReviewService) Decide(ctx context.Context, adminID, verificationID uuid.UUID, decision string) error {
	if decision != decisionApprove && decision != decisionReject {
		return ErrInvalidDecision
	}
	if err := service.store.DecideReview(ctx, adminID, verificationID, decision == decisionApprove, service.now().UTC()); err != nil {
		return err
	}
	slog.Info("ID check decided", "verification_id", verificationID, "admin_id", adminID, "decision", decision)
	return nil
}

func (store *PostgresVerificationStore) PendingReviews(ctx context.Context, adminID uuid.UUID, limit int) ([]PendingReview, error) {
	reviews := []PendingReview{}
	err := db.WithUserTx(ctx, store.DB, adminID.String(), func(tx pgx.Tx) error {
		var admin bool
		if err := tx.QueryRow(ctx, `SELECT app.is_admin()`).Scan(&admin); err != nil {
			return fmt.Errorf("check admin: %w", err)
		}
		if !admin {
			return ErrNotAdmin
		}
		rows, err := tx.Query(ctx,
			`SELECT verification_id, account_id, persona_inquiry_id, reasons, completed_at, new_name, previous_name
			 FROM app.pending_identity_reviews($1)`, limit)
		if err != nil {
			return fmt.Errorf("list ID reviews: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var review PendingReview
			var reasons []string
			if err := rows.Scan(&review.VerificationID, &review.AccountID, &review.InquiryID, &reasons,
				&review.CompletedAt, &review.NewName, &review.PreviousName); err != nil {
				return fmt.Errorf("scan ID review: %w", err)
			}
			for _, reason := range reasons {
				review.Reasons = append(review.Reasons, ReviewReason(reason))
			}
			reviews = append(reviews, review)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return reviews, nil
}

func (store *PostgresVerificationStore) RejectedReviews(ctx context.Context, adminID uuid.UUID, limit int) ([]RejectedReview, error) {
	reviews := []RejectedReview{}
	err := db.WithUserTx(ctx, store.DB, adminID.String(), func(tx pgx.Tx) error {
		var admin bool
		if err := tx.QueryRow(ctx, `SELECT app.is_admin()`).Scan(&admin); err != nil {
			return fmt.Errorf("check admin: %w", err)
		}
		if !admin {
			return ErrNotAdmin
		}
		rows, err := tx.Query(ctx,
			`SELECT verification_id, account_id, persona_inquiry_id, reasons, completed_at, new_name, previous_name,
			        rejected_at, automatic, erased
			 FROM app.rejected_identity_reviews($1)`, limit)
		if err != nil {
			return fmt.Errorf("list rejected ID checks: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var review RejectedReview
			var reasons []string
			if err := rows.Scan(&review.VerificationID, &review.AccountID, &review.InquiryID, &reasons,
				&review.CompletedAt, &review.NewName, &review.PreviousName,
				&review.RejectedAt, &review.Automatic, &review.Erased); err != nil {
				return fmt.Errorf("scan rejected ID check: %w", err)
			}
			for _, reason := range reasons {
				review.Reasons = append(review.Reasons, ReviewReason(reason))
			}
			reviews = append(reviews, review)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return reviews, nil
}

func (store *PostgresVerificationStore) OverturnReview(ctx context.Context, adminID, verificationID uuid.UUID, note string, at time.Time) error {
	return store.adminOutcome(ctx, adminID, "approve rejected ID check",
		`SELECT app.overturn_identity_review($1, $2, $3)`, verificationID, note, at)
}

func (store *PostgresVerificationStore) EraseRejected(ctx context.Context, adminID, verificationID uuid.UUID, at time.Time) error {
	return store.adminOutcome(ctx, adminID, "erase rejected ID check",
		`SELECT app.erase_rejected_identity($1, $2)`, verificationID, at)
}

func (store *PostgresVerificationStore) adminOutcome(ctx context.Context, adminID uuid.UUID, action, query string, args ...any) error {
	return db.WithUserTx(ctx, store.DB, adminID.String(), func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO app.accounts (id) VALUES ($1) ON CONFLICT (id) DO NOTHING`, adminID); err != nil {
			return fmt.Errorf("ensure account: %w", err)
		}
		var outcome string
		if err := tx.QueryRow(ctx, query, args...).Scan(&outcome); err != nil {
			return fmt.Errorf("%s: %w", action, err)
		}
		mapped, known := reviewOutcomes[outcome]
		if !known {
			return fmt.Errorf("%s: unknown outcome %q", action, outcome)
		}
		return mapped
	})
}

func (store *PostgresVerificationStore) DecideReview(ctx context.Context, adminID, verificationID uuid.UUID, approve bool, at time.Time) error {
	return db.WithUserTx(ctx, store.DB, adminID.String(), func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO app.accounts (id) VALUES ($1) ON CONFLICT (id) DO NOTHING`, adminID); err != nil {
			return fmt.Errorf("ensure account: %w", err)
		}
		var outcome string
		if err := tx.QueryRow(ctx, `SELECT app.decide_identity_review($1, $2, $3)`, verificationID, approve, at).Scan(&outcome); err != nil {
			return fmt.Errorf("decide ID review: %w", err)
		}
		mapped, known := reviewOutcomes[outcome]
		if !known {
			return fmt.Errorf("decide ID review: unknown outcome %q", outcome)
		}
		return mapped
	})
}

func (handler *HTTPHandler) WithReviews(reviews IdentityReviewer) *HTTPHandler {
	handler.reviews = reviews
	return handler
}

func (handler *HTTPHandler) ListReviews(w http.ResponseWriter, r *http.Request) {
	adminID, ok := accounts.UserID(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	if handler.reviews == nil {
		writeError(w, http.StatusNotFound, "ID reviews are not available")
		return
	}
	var reviews any
	var err error
	if r.URL.Query().Get("status") == statusRejected {
		reviews, err = handler.reviews.Rejected(r.Context(), adminID)
	} else {
		reviews, err = handler.reviews.Pending(r.Context(), adminID)
	}
	if err != nil {
		writeReviewError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]any{"reviews": reviews})
}

func (handler *HTTPHandler) DecideReview(w http.ResponseWriter, r *http.Request) {
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
	var request struct {
		Decision string `json:"decision"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxDecisionBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid JSON request")
		return
	}
	if err := handler.reviews.Decide(r.Context(), adminID, verificationID, request.Decision); err != nil {
		writeReviewError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (handler *HTTPHandler) OverturnReview(w http.ResponseWriter, r *http.Request) {
	adminID, verificationID, ok := handler.reviewTarget(w, r)
	if !ok {
		return
	}
	var request struct {
		Note string `json:"note"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxOverturnBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid JSON request")
		return
	}
	if err := handler.reviews.Overturn(r.Context(), adminID, verificationID, request.Note); err != nil {
		writeReviewError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (handler *HTTPHandler) EraseReview(w http.ResponseWriter, r *http.Request) {
	adminID, verificationID, ok := handler.reviewTarget(w, r)
	if !ok {
		return
	}
	if err := handler.reviews.Erase(r.Context(), adminID, verificationID); err != nil {
		writeReviewError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (handler *HTTPHandler) reviewTarget(w http.ResponseWriter, r *http.Request) (uuid.UUID, uuid.UUID, bool) {
	adminID, ok := accounts.UserID(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return uuid.Nil, uuid.Nil, false
	}
	verificationID, err := uuid.Parse(r.PathValue("verification_id"))
	if handler.reviews == nil || err != nil {
		writeError(w, http.StatusNotFound, "verification not found")
		return uuid.Nil, uuid.Nil, false
	}
	return adminID, verificationID, true
}

func writeReviewError(w http.ResponseWriter, err error) {
	for _, mapped := range []struct {
		err    error
		status int
	}{
		{ErrNotAdmin, http.StatusForbidden},
		{ErrOwnVerification, http.StatusForbidden},
		{ErrReviewNotFound, http.StatusNotFound},
		{ErrNotAwaitingReview, http.StatusConflict},
		{ErrInvalidDecision, http.StatusBadRequest},
		{ErrNoteRequired, http.StatusBadRequest},
		{ErrIDErased, http.StatusConflict},
		{ErrSuperseded, http.StatusConflict},
	} {
		if errors.Is(err, mapped.err) {
			writeError(w, mapped.status, mapped.err.Error())
			return
		}
	}
	slog.Error("ID review request failed", "error", err)
	writeError(w, http.StatusInternalServerError, "something went wrong")
}
