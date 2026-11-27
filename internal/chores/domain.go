package chores

const (
	DefaultAmountCents = 5000
	MaxAmountCents     = 1_000_000
	ownChoreHint       = "Show it done."
)

type Chore struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Note     string `json:"note,omitempty"`
	PhotoKey string `json:"photo_key,omitempty"`
	PhotoURL string `json:"photo_url,omitempty"`
}

type Group struct {
	Key         string  `json:"key"`
	Label       string  `json:"label,omitempty"`
	AmountCents int     `json:"amount_cents"`
	Chores      []Chore `json:"chores"`
}

type Plan []Group

type standardChore struct {
	key   string
	label string
	hint  string
}

type standardGroup struct {
	key    string
	label  string
	chores []standardChore
}
