package listings

type deleteRefusal string

func (refusal deleteRefusal) Error() string { return string(refusal) }

func (refusal deleteRefusal) Is(target error) bool { return target == ErrListingInUse }

const (
	ErrDeletePaused      = deleteRefusal("a paused listing cannot be deleted")
	ErrDeleteHasBookings = deleteRefusal("a listing with bookings cannot be deleted")
	ErrDeleteHasMessages = deleteRefusal("a listing guests have messaged you about cannot be deleted")
	ErrDeleteProofHeld   = deleteRefusal("its ownership proof was withdrawn in the last 90 days, so it is kept until then")
)
