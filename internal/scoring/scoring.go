// Package scoring holds the dispatch ranking functions. Everything here is
// deliberately PURE: plain input -> output, with no DB, clock, randomness or I/O.
// That is what makes the ranking (a) trivially unit-testable with no mocks and
// (b) EXPLAINABLE — every score is a weighted sum of named components, each of
// which is surfaced in a Breakdown so an operator can reconstruct exactly why one
// unit outranked another.
package scoring

import (
	"github.com/AtharvGupta360/CrisisLink/internal/incident"
	"github.com/AtharvGupta360/CrisisLink/internal/shelter"
	"github.com/AtharvGupta360/CrisisLink/internal/unit"
)

// --- Travel model -----------------------------------------------------------
//
// HONEST LIMITATION, stated up front: this is a GEOMETRIC travel model, not a
// routed one. There is no road graph, no turn restrictions, no live traffic. It
// converts straight-line distance into a plausible travel time, which is enough to
// rank candidates sensibly and to explain the ranking — and it is deliberately
// isolated in routingDistanceMeters/etaSeconds so that swapping in a real routing
// engine (OSRM, Valhalla) replaces two small functions and nothing else.
//
// Do not describe this as road-network routing. It is a defensible proxy.

// roadDetourFactor inflates great-circle distance toward real road distance.
// Straight-line always UNDERESTIMATES travel: roads bend, rivers need bridges,
// one-way systems force detours. ~1.3x is the widely used urban rule of thumb.
const roadDetourFactor = 1.3

// avgSpeedMPS is the assumed average response speed per unit type, in metres per
// second. These differ on purpose: a laden fire appliance does not move like an
// ambulance, so at EQUAL DISTANCE the faster type gets a better ETA. That
// difference is the entire reason ETA earns its place alongside distance.
var avgSpeedMPS = map[string]float64{
	unit.UnitTypeAmbulance: 13.9, // ~50 km/h
	unit.UnitTypePolice:    16.7, // ~60 km/h, lighter and more agile
	unit.UnitTypeFire:      11.1, // ~40 km/h, heavy appliance
	unit.UnitTypeRescue:    11.1, // ~40 km/h, heavy equipment
}

const defaultSpeedMPS = 11.1 // conservative fallback for an unknown type

// etaFalloffSeconds is the response time at which the time component decays to 0.
// 15 minutes: beyond that a unit is not meaningfully "fast", though it can still
// win on skill if it is the only appropriate responder.
const etaFalloffSeconds = 900.0

// partialSkillMatch is the credit a non-matching unit receives. Greater than 0
// because the wrong kind of help still beats no help; less than 1 so the right
// specialisation always wins, all else equal.
const partialSkillMatch = 0.30

// routingDistanceMeters converts straight-line distance to an estimated road
// distance. Isolated so a real routing engine can replace it.
func routingDistanceMeters(straightLineMeters float64) float64 {
	return straightLineMeters * roadDetourFactor
}

// etaSeconds estimates arrival time for a unit type over a routed distance.
func etaSeconds(routedMeters float64, unitType string) float64 {
	speed, ok := avgSpeedMPS[unitType]
	if !ok || speed <= 0 {
		speed = defaultSpeedMPS
	}
	return routedMeters / speed
}

// --- Severity-driven weighting ----------------------------------------------

// Weights split the decision between arriving FAST and arriving RIGHT. They always
// sum to 1.0, so a score stays in [0,1] and the numbers read as percentages.
type Weights struct {
	Time  float64 `json:"time"`
	Skill float64 `json:"skill"`
}

// weightsForSeverity is where incident severity actually influences dispatch.
//
// THE SUBTLETY WORTH UNDERSTANDING: severity is a property of the INCIDENT, so it
// is identical for every candidate unit. Adding it as another weighted term would
// shift every candidate's score by the same constant and change the ranking by
// exactly nothing. Severity is therefore not a COMPONENT — it is a MODULATOR that
// re-balances the components that do differ between units.
//
// The policy it encodes: the more critical the incident, the more we take whoever
// can get there soonest; the less critical, the more we can afford to wait for the
// properly specialised unit.
func weightsForSeverity(severity string) Weights {
	switch severity {
	case incident.SeverityCritical:
		return Weights{Time: 0.85, Skill: 0.15} // life-threatening: speed dominates
	case incident.SeverityHigh:
		return Weights{Time: 0.75, Skill: 0.25}
	case incident.SeverityMedium:
		return Weights{Time: 0.60, Skill: 0.40}
	default: // low, or unknown severity
		return Weights{Time: 0.50, Skill: 0.50} // send the right tool for the job
	}
}

// --- Unit scoring -----------------------------------------------------------

// Need describes what an incident requires. Passing a struct (rather than a widening
// list of arguments) keeps the signature stable as more factors are added.
type Need struct {
	// Severity re-weights speed vs specialisation. See weightsForSeverity.
	Severity string
	// RequiredType is the specialisation the incident calls for
	// (ambulance/fire/rescue/police). Empty means no preference, which makes the
	// skill component neutral and reduces the score to pure speed.
	RequiredType string
}

// Breakdown is the explainable trace of one score. Components are the values
// BEFORE weighting, alongside the raw inputs, so the final number can be
// reconstructed by hand: Score = Weights.Time*TimeScore + Weights.Skill*SkillScore.
type Breakdown struct {
	TimeScore  float64 `json:"timeScore"`  // 0..1, sooner = higher
	SkillScore float64 `json:"skillScore"` // 0..1, required specialisation = 1.0
	Weights    Weights `json:"weights"`    // the severity-derived split actually used

	DistanceMeters        float64 `json:"distanceMeters"`        // straight line, as measured
	RoutingDistanceMeters float64 `json:"routingDistanceMeters"` // detour-adjusted estimate
	ETASeconds            float64 `json:"etaSeconds"`            // estimated arrival time

	Severity     string `json:"severity,omitempty"`
	RequiredType string `json:"requiredType,omitempty"`
	UnitType     string `json:"unitType"`
}

// ScoredUnit pairs a unit with its dispatch score and the breakdown behind it.
// This is what the API returns instead of a bare unit list — the "explainable" part.
type ScoredUnit struct {
	Unit      unit.Unit `json:"unit"`
	Score     float64   `json:"score"`
	Breakdown Breakdown `json:"breakdown"`
}

// ScoreUnit computes a unit's dispatch score for an incident. PURE.
//
// The incident enters through u.DistanceMeters, which the candidate search already
// measured relative to that incident (live Redis position when available, the
// PostGIS registration pin otherwise), plus the Need describing what it requires.
//
// Availability is NOT scored here: an unavailable unit is filtered out before
// scoring, because it is a hard constraint, not a preference. Scoring only ever
// ranks candidates that are already legal to dispatch.
func ScoreUnit(u *unit.Unit, need Need) (float64, Breakdown) {
	routed := routingDistanceMeters(u.DistanceMeters)
	eta := etaSeconds(routed, u.Type)

	// Time: linear decay from 1.0 (already on scene) to 0.0 at the falloff.
	// Clamped so a very distant unit scores 0 rather than going negative.
	timeScore := 1.0 - eta/etaFalloffSeconds
	if timeScore < 0 {
		timeScore = 0
	}

	// Skill: full credit for the required specialisation, partial for anything
	// else. No requirement => neutral 1.0, so the score reduces to pure speed.
	skillScore := 1.0
	if need.RequiredType != "" && u.Type != need.RequiredType {
		skillScore = partialSkillMatch
	}

	w := weightsForSeverity(need.Severity)
	total := w.Time*timeScore + w.Skill*skillScore

	return total, Breakdown{
		TimeScore:             timeScore,
		SkillScore:            skillScore,
		Weights:               w,
		DistanceMeters:        u.DistanceMeters,
		RoutingDistanceMeters: routed,
		ETASeconds:            eta,
		Severity:              need.Severity,
		RequiredType:          need.RequiredType,
		UnitType:              u.Type,
	}
}

// --- Shelter scoring --------------------------------------------------------

// shelterProximityFalloffMeters is the distance at which a shelter's proximity
// component decays to 0. Wider than the unit falloff: moving survivors a longer
// way to a shelter with room is normal, whereas a slow rescue unit is not.
const shelterProximityFalloffMeters = 25000.0

const (
	weightShelterProximity = 0.70
	weightShelterHeadroom  = 0.30
)

// ShelterBreakdown explains a shelter allocation score.
type ShelterBreakdown struct {
	Proximity      float64 `json:"proximity"` // 0..1, closer = higher
	Headroom       float64 `json:"headroom"`  // 0..1, share of capacity still free
	DistanceMeters float64 `json:"distanceMeters"`
	AvailableSpots int     `json:"availableSpots"`
	Capacity       int     `json:"capacity"`
}

// ScoredShelter pairs a shelter with its allocation score and breakdown.
type ScoredShelter struct {
	Shelter   shelter.Shelter  `json:"shelter"`
	Score     float64          `json:"score"`
	Breakdown ShelterBreakdown `json:"breakdown"`
}

// ScoreShelter ranks a shelter for a victim allocation. PURE.
//
// HEADROOM is why capacity belongs in this score and not in the unit score. Two
// shelters at the same distance are not equally good if one has 1 bed left and the
// other has 40: sending people to the nearly-full one produces a burst of failed
// admissions (the P18 capacity guard rejecting them) and leaves no slack for a
// family that must stay together. Preferring headroom spreads load across shelters
// and makes a successful admission more likely.
//
// Headroom is a RATIO, not an absolute count, so a 10-bed shelter with 5 free
// scores the same as a 200-bed shelter with 100 free — it measures how much room
// remains relative to the shelter's own size, which is what "is this filling up"
// actually means.
func ScoreShelter(sh *shelter.Shelter) (float64, ShelterBreakdown) {
	prox := 1.0 - sh.DistanceMeters/shelterProximityFalloffMeters
	if prox < 0 {
		prox = 0
	}

	headroom := 0.0
	if sh.Capacity > 0 {
		headroom = float64(sh.Capacity-sh.Occupancy) / float64(sh.Capacity)
	}
	if headroom < 0 {
		headroom = 0 // over-capacity should be impossible (CHECK constraint), but never trust it
	}

	total := weightShelterProximity*prox + weightShelterHeadroom*headroom

	return total, ShelterBreakdown{
		Proximity:      prox,
		Headroom:       headroom,
		DistanceMeters: sh.DistanceMeters,
		AvailableSpots: sh.Capacity - sh.Occupancy,
		Capacity:       sh.Capacity,
	}
}
