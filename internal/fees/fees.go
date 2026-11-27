package fees

const (
	PercentOfStay = 8
	FixedCents    = 30
	percent       = 100
	halfCent      = percent / 2
)

func Total(stayCents int) int {
	if stayCents <= 0 {
		return 0
	}
	return (stayCents*PercentOfStay+halfCent)/percent + FixedCents
}

func HostShare(keptCents int) int {
	return min(Total(keptCents), max(keptCents, 0))
}
