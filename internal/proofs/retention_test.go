package proofs

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

type retentionStub struct {
	due    []DueFile
	marked []uuid.UUID
}

func (store *retentionStub) FilesDueForDeletion(context.Context, int) ([]DueFile, error) {
	return store.due, nil
}

func (store *retentionStub) MarkFileDeleted(_ context.Context, proofID uuid.UUID) error {
	store.marked = append(store.marked, proofID)
	return nil
}

func (store *retentionStub) ExpireAuthority(context.Context, time.Time) (int, error) { return 0, nil }

type failingRemover string

func (failing failingRemover) Remove(_ context.Context, key string) error {
	if key == string(failing) {
		return errors.New("storage offline")
	}
	return nil
}

func TestAFileThatCannotBeRemovedStaysDue(t *testing.T) {
	kept, removed := DueFile{ProofID: uuid.New(), ObjectKey: "kept"}, DueFile{ProofID: uuid.New(), ObjectKey: "removed"}
	store := &retentionStub{due: []DueFile{kept, removed}}

	deleted, err := NewFileRetentionService(store, failingRemover("kept")).RunOnce(context.Background())

	if err != nil || deleted != 1 || len(store.marked) != 1 || store.marked[0] != removed.ProofID {
		t.Fatalf("deleted %d, marked %v, error %v", deleted, store.marked, err)
	}
}
