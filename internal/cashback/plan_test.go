package cashback

import (
	"context"
	"errors"
	"testing"

	"github.com/BetterGuests/BetterGuests-Server/internal/chores"
	"github.com/google/uuid"
)

func kitchenOnlyPlan(amountCents int, labels ...string) chores.Plan {
	group := chores.Group{Key: "kitchen", AmountCents: amountCents}
	for _, label := range labels {
		group.Chores = append(group.Chores, chores.Chore{Label: label, Note: "Use the blue cloth."})
	}
	return chores.Plan{group}
}

func (h harness) savePlan(t *testing.T, plan chores.Plan) PlanView {
	t.Helper()
	saved, err := h.service.SaveListingPlan(context.Background(), h.hostID, uuid.New(), plan)
	if err != nil {
		t.Fatalf("SaveListingPlan() error = %v", err)
	}
	return saved
}

func TestAGuestEarnsTheHostsAmountForTheHostsOwnChores(t *testing.T) {
	h := newHarness(true)
	saved := h.savePlan(t, kitchenOnlyPlan(12000, "Water the plants"))
	h.store.bookingPlan = h.store.listingPlan
	own := saved.Groups[0].Chores[0].Key

	h.submit(t, own)
	trip, err := h.service.Trip(context.Background(), h.guestID, h.booking)

	if err != nil || trip.ExpectedCents != 12000 || len(trip.Bundles) != 1 {
		t.Fatalf("trip = %+v, err = %v", trip, err)
	}
	if task := trip.Bundles[0].Tasks[0]; task.Instructions != "Use the blue cloth." || task.Hint != "Show it done." {
		t.Fatalf("task = %+v", task)
	}
}

func TestAChoreTheHostRemovedCannotBeSubmitted(t *testing.T) {
	h := newHarness(true)
	h.savePlan(t, kitchenOnlyPlan(5000, "Water the plants"))
	h.store.bookingPlan = h.store.listingPlan

	_, err := h.service.SubmitPhoto(context.Background(), h.guestID, h.booking, "strip_beds", h.key("strip_beds"))

	if !errors.Is(err, ErrUnknownTask) {
		t.Fatalf("error = %v", err)
	}
}

func TestAnyAmountIsStillCappedAtAQuarterOfTheStay(t *testing.T) {
	h := newHarness(true)
	saved := h.savePlan(t, kitchenOnlyPlan(90000, "Water the plants"))

	got := EarnableCents(saved.Groups, []string{saved.Groups[0].Chores[0].Key}, 60000)

	if got != 15000 {
		t.Fatalf("cents = %d", got)
	}
}

func TestAnInvalidPlanIsRefused(t *testing.T) {
	cases := map[string]chores.Plan{
		"money with no chore": {{Key: "kitchen", AmountCents: 100}},
		"negative amount":     kitchenOnlyPlan(-1, "Sweep"),
		"blank chore":         kitchenOnlyPlan(100, "   "),
		"unknown group":       {{Key: "garage"}},
		"repeated group":      {{Key: "kitchen"}, {Key: "kitchen"}},
	}
	for name, plan := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHarness(true)
			if _, err := h.service.SaveListingPlan(context.Background(), h.hostID, uuid.New(), plan); !errors.Is(err, ErrInvalidPlan) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestAnExamplePhotoFromAnotherListingIsRefused(t *testing.T) {
	h := newHarness(true)
	plan := kitchenOnlyPlan(100, "Sweep")
	plan[0].Chores[0].PhotoKey = choreKeyPrefix(uuid.New()) + uuid.NewString() + ".jpg"

	_, err := h.service.SaveListingPlan(context.Background(), h.hostID, uuid.New(), plan)

	if !errors.Is(err, ErrInvalidPhotoKey) {
		t.Fatalf("error = %v", err)
	}
}
