package chores

import (
	"encoding/json"
	"fmt"
)

var catalog = []standardGroup{
	{key: "kitchen", label: "Kitchen", chores: []standardChore{
		{key: "run_dishwasher", label: "Run the dishwasher", hint: "Show the loaded racks or the running display."},
		{key: "empty_fridge", label: "Empty the fridge", hint: "Open the fridge door, turn on the kitchen light, and show every shelf."},
		{key: "wipe_counters", label: "Wipe the counters", hint: "Show the kitchen counters, clear and wiped."},
	}},
	{key: "beds_towels", label: "Beds & towels", chores: []standardChore{
		{key: "strip_beds", label: "Strip the beds", hint: "Show a bare mattress and the linens in the hamper."},
		{key: "towels_in_hamper", label: "Towels in the hamper", hint: "Show the used towels in the hamper."},
	}},
	{key: "lock_up", label: "Lock-up", chores: []standardChore{
		{key: "take_out_trash", label: "Take out the trash", hint: "Show the bag inside the outdoor bin, lid open."},
		{key: "lights_thermostat", label: "Lights off, thermostat 72°", hint: "Show the thermostat set to 72°."},
	}},
}

func DefaultPlan() Plan {
	plan := make(Plan, 0, len(catalog))
	for _, standard := range catalog {
		group := Group{Key: standard.key, Label: standard.label, AmountCents: DefaultAmountCents}
		for _, chore := range standard.chores {
			group.Chores = append(group.Chores, Chore{Key: chore.key, Label: chore.label})
		}
		plan = append(plan, group)
	}
	return plan
}

func PlanFromJSON(raw []byte) (Plan, error) {
	if len(raw) == 0 {
		return DefaultPlan(), nil
	}
	var plan Plan
	if err := json.Unmarshal(raw, &plan); err != nil {
		return nil, fmt.Errorf("read chore plan: %w", err)
	}
	for index := range plan {
		plan[index].Label = GroupLabel(plan[index].Key)
	}
	return plan, nil
}

func KnownGroup(groupKey string) bool {
	for _, standard := range catalog {
		if standard.key == groupKey {
			return true
		}
	}
	return false
}

func GroupLabel(groupKey string) string {
	for _, standard := range catalog {
		if standard.key == groupKey {
			return standard.label
		}
	}
	return groupKey
}

func HintFor(chore Chore) string {
	for _, group := range catalog {
		for _, standard := range group.chores {
			if standard.key == chore.Key && standard.label == chore.Label {
				return standard.hint
			}
		}
	}
	return ownChoreHint
}

func (plan Plan) Offered() Plan {
	offered := Plan{}
	for _, group := range plan {
		if group.AmountCents > 0 && len(group.Chores) > 0 {
			offered = append(offered, group)
		}
	}
	return offered
}

func (plan Plan) Find(taskKey string) (Chore, bool) {
	for _, group := range plan.Offered() {
		for _, chore := range group.Chores {
			if chore.Key == taskKey {
				return chore, true
			}
		}
	}
	return Chore{}, false
}
