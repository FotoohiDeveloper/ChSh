package domain

import "fmt"

type Status string

const (
	Ready         Status = "ready"
	WithPilgrim   Status = "with_pilgrim"
	Dirty         Status = "dirty"
	InWash        Status = "in_wash"
	PendingReview Status = "pending_review"
	Repair        Status = "repair"
	Retired       Status = "retired"
)

func (s Status) Valid() bool {
	switch s {
	case Ready, WithPilgrim, Dirty, InWash, PendingReview, Repair, Retired:
		return true
	}
	return false
}

type edge struct{ from, to Status }

// transitions is the single source of truth for who may move a tent from one
// status to another with the generic "set status" operation. Handing a tent
// to a pilgrim and taking it back have their own operations (assignments), so
// ready->with_pilgrim and with_pilgrim->* are not listed here.
var transitions = map[edge][]Role{
	{Ready, Dirty}:          {Server, GateLead, Team},
	{Ready, Repair}:         {Server, GateLead, Team},
	{Dirty, InWash}:         {Workshop},
	{Dirty, Repair}:         {Team},
	{InWash, PendingReview}: {Workshop},
	{PendingReview, Ready}:  {Team, GateLead},
	{PendingReview, Dirty}:  {Team, GateLead},
	{PendingReview, Repair}: {Team},
	{Repair, PendingReview}: {Team},
	{Retired, Ready}:        {Admin},
}

// Statuses that Admin/Office may force a tent into, whatever its current one,
// except for a tent that is currently with a pilgrim.
func officeOverride(to Status) bool {
	return to == Retired || to == Ready || to == Dirty || to == Repair
}

type TransitionError struct {
	From, To Status
	Role     Role
	Reason   string
}

func (e *TransitionError) Error() string {
	return fmt.Sprintf("transition %s -> %s by %s: %s", e.From, e.To, e.Role, e.Reason)
}

// CheckTransition validates a generic status change.
func CheckTransition(role Role, from, to Status) error {
	if from == to {
		return &TransitionError{from, to, role, "status unchanged"}
	}
	if from == WithPilgrim || to == WithPilgrim {
		return &TransitionError{from, to, role, "use assignment operations for tents with pilgrims"}
	}
	if (role == Admin || role == Office) && officeOverride(to) && !(role == Office && from == Retired) {
		return nil
	}
	for _, r := range transitions[edge{from, to}] {
		if r == role {
			return nil
		}
	}
	return &TransitionError{from, to, role, "not allowed"}
}

// ReturnTargets are the statuses a tent can be given when a pilgrim returns it.
func ValidReturnStatus(s Status) bool {
	return s == Ready || s == Dirty || s == Repair
}
