package dispatch

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/AtharvGupta360/CrisisLink/internal/incident"
	"github.com/AtharvGupta360/CrisisLink/internal/outbox"
	"github.com/AtharvGupta360/CrisisLink/internal/platform/common"
)

// reassignCandidateLimit bounds how many alternatives are considered before giving
// up. Each attempt costs a transaction, so trying every unit in the fleet during a
// contention storm would be worse than admitting defeat and letting the operator
// decide.
const reassignCandidateLimit = 5

// Reroute moves an incident's dispatch to a different unit — used when the assigned
// unit fails: it breaks down, goes dark, or is taken out of service.
//
// THE PATTERN: candidate selection (geospatial search + scoring) happens OUTSIDE
// the transaction, because it is a read and a computation that would hold locks for
// no reason. The commitment is then verified INSIDE the transaction, under the
// unit's lock. If the chosen unit was taken in between, Reassign returns
// ErrUnitNotAvailable and we simply try the next-best candidate.
//
// That is optimistic selection with pessimistic commitment, and it is what keeps
// the lock window short while still being safe.
func (s *DispatchService) Reroute(ctx context.Context, dispatchID string) (*Dispatch, error) {
	old, err := s.dispatches.GetByID(ctx, dispatchID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrDispatchNotFound
		}
		return nil, err
	}
	if !IsActiveDispatch(old.Status) {
		return nil, ErrIllegalDispatchTransition
	}

	// Rank alternatives exactly as the original dispatch would have.
	_, candidates, _, err := s.Candidates(ctx, old.IncidentID, "", reassignCandidateLimit)
	if err != nil {
		return nil, err
	}

	for i := range candidates {
		cand := candidates[i].Unit
		if cand.ID == old.UnitID {
			continue // rerouting to the same unit would be a no-op
		}
		d, rerr := s.dispatches.Reassign(ctx, dispatchID, old.IncidentID, cand.ID, outbox.EventDispatchRerouted)
		if rerr == nil {
			common.Logger.Infow("dispatch rerouted",
				"dispatchId", dispatchID, "fromUnit", old.UnitID, "toUnit", cand.ID)
			return d, nil
		}
		// Lost the race for this candidate — someone reserved it between our scoring
		// pass and our lock. Try the next one rather than failing the whole reroute.
		if errors.Is(rerr, ErrUnitNotAvailable) {
			continue
		}
		return nil, rerr
	}
	return nil, ErrNoReassignCandidate
}

// Preempt takes a unit away from a LESS SEVERE incident and gives it to this one.
//
// It is the escalation of last resort: called when a critical incident has no free
// unit at all. The unit does not move to a different vehicle — the SAME unit is
// reassigned to a more urgent incident, and the incident it was serving reverts to
// 'verified' so it resurfaces as needing help.
//
// This is a policy decision encoded in software, and it is worth being explicit
// that it is a tradeoff, not an optimisation: somebody's ambulance is being taken
// away. The strict severity comparison (only ever preempt something LESS severe)
// is what stops it from cycling — two equal-severity incidents can never preempt
// each other back and forth.
func (s *DispatchService) Preempt(ctx context.Context, incidentID string) (*Dispatch, error) {
	inc, err := s.incidents.GetByID(ctx, incidentID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, incident.ErrIncidentNotFound
		}
		return nil, err
	}
	if inc.Status == incident.StatusResolved || inc.Status == incident.StatusCancelled {
		return nil, ErrIncidentNotDispatchable
	}

	victims, err := s.dispatches.FindPreemptable(ctx, inc.Severity, inc.Latitude, inc.Longitude, reassignCandidateLimit)
	if err != nil {
		return nil, err
	}

	for _, v := range victims {
		// Same unit, new incident. Reassign holds the dispatch and unit locks and
		// re-validates, so a victim dispatch that completed in the meantime is
		// rejected rather than silently resurrected.
		d, rerr := s.dispatches.Reassign(ctx, v.DispatchID, incidentID, v.UnitID, outbox.EventDispatchPreempted)
		if rerr == nil {
			common.Logger.Warnw("dispatch PREEMPTED for a more severe incident",
				"unit", v.CallSign, "fromIncident", v.IncidentID, "fromSeverity", v.Severity,
				"toIncident", incidentID, "toSeverity", inc.Severity)
			return d, nil
		}
		// The victim finished or was reassigned first — move to the next candidate.
		if errors.Is(rerr, ErrIllegalDispatchTransition) || errors.Is(rerr, ErrUnitNotAvailable) {
			continue
		}
		return nil, rerr
	}
	return nil, ErrNoReassignCandidate
}

// PreemptableFor lists what COULD be taken for this incident, without taking it.
// Preemption harms someone, so an operator should be able to see the cost first.
func (s *DispatchService) PreemptableFor(ctx context.Context, incidentID string) (*incident.Incident, []Preemptable, error) {
	inc, err := s.incidents.GetByID(ctx, incidentID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil, incident.ErrIncidentNotFound
		}
		return nil, nil, err
	}
	v, err := s.dispatches.FindPreemptable(ctx, inc.Severity, inc.Latitude, inc.Longitude, reassignCandidateLimit)
	if err != nil {
		return nil, nil, err
	}
	return inc, v, nil
}
