package scoring

import (
	"math"
	"sort"
	"testing"

	"github.com/AtharvGupta360/CrisisLink/internal/models"
)

// eps is the float tolerance for comparisons — floats are never exactly equal.
const eps = 1e-9

func almostEqual(a, b float64) bool { return math.Abs(a-b) < eps }

// unit is a tiny constructor so the table stays readable.
func unit(typ string, dist float64) *models.Unit {
	return &models.Unit{Type: typ, Status: models.UnitAvailable, DistanceMeters: dist}
}

func TestScore(t *testing.T) {
	tests := []struct {
		name          string
		unit          *models.Unit
		preferredType string
		wantProx      float64 // expected proximity component (pre-weight)
		wantTypeMatch float64 // expected type-match component (pre-weight)
		wantScore     float64 // expected final weighted score
	}{
		{
			name:          "on top of the incident, no preference => perfect",
			unit:          unit(models.UnitTypeAmbulance, 0),
			preferredType: "",
			wantProx:      1.0,
			wantTypeMatch: 1.0,
			wantScore:     1.0, // 0.70*1 + 0.30*1
		},
		{
			name:          "halfway to falloff, no preference",
			unit:          unit(models.UnitTypeFire, 5000),
			preferredType: "",
			wantProx:      0.5,
			wantTypeMatch: 1.0,
			wantScore:     0.70*0.5 + 0.30*1.0, // 0.65
		},
		{
			name:          "preferred type gets full type-match",
			unit:          unit(models.UnitTypeAmbulance, 2000),
			preferredType: models.UnitTypeAmbulance,
			wantProx:      0.8,
			wantTypeMatch: 1.0,
			wantScore:     0.70*0.8 + 0.30*1.0, // 0.86
		},
		{
			name:          "wrong type gets partial credit, not zero",
			unit:          unit(models.UnitTypeFire, 2000),
			preferredType: models.UnitTypeAmbulance,
			wantProx:      0.8,
			wantTypeMatch: partialTypeMatch,
			wantScore:     0.70*0.8 + 0.30*partialTypeMatch, // 0.65
		},
		{
			name:          "beyond falloff => proximity clamps to 0",
			unit:          unit(models.UnitTypeRescue, 15000),
			preferredType: "",
			wantProx:      0.0,
			wantTypeMatch: 1.0,
			wantScore:     0.30, // 0.70*0 + 0.30*1
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, bd := Score(tc.unit, tc.preferredType)
			if !almostEqual(bd.Proximity, tc.wantProx) {
				t.Errorf("proximity = %v, want %v", bd.Proximity, tc.wantProx)
			}
			if !almostEqual(bd.TypeMatch, tc.wantTypeMatch) {
				t.Errorf("typeMatch = %v, want %v", bd.TypeMatch, tc.wantTypeMatch)
			}
			if !almostEqual(got, tc.wantScore) {
				t.Errorf("score = %v, want %v", got, tc.wantScore)
			}
		})
	}
}

// TestScore_PreferredTypeCanOvertakeCloserUnit is the explainability payoff: a
// preferred-type unit slightly farther away should outrank a closer wrong-type
// unit. This is the behavior that justifies scoring over raw nearest-first.
func TestScore_PreferredTypeCanOvertakeCloserUnit(t *testing.T) {
	closerWrongType := unit(models.UnitTypeFire, 500)       // very close, wrong kind
	fartherRightType := unit(models.UnitTypeAmbulance, 900) // a bit farther, ideal kind

	sClose, _ := Score(closerWrongType, models.UnitTypeAmbulance)
	sFar, _ := Score(fartherRightType, models.UnitTypeAmbulance)

	if sFar <= sClose {
		t.Fatalf("expected preferred-type unit (%.4f) to outrank closer wrong-type unit (%.4f)", sFar, sClose)
	}
}

// TestScore_NoPreferenceIsPureProximity confirms that with no preferred type,
// ranking collapses to nearest-first (type is neutral).
func TestScore_NoPreferenceIsPureProximity(t *testing.T) {
	near := unit(models.UnitTypeFire, 100)
	far := unit(models.UnitTypeAmbulance, 3000)

	sNear, _ := Score(near, "")
	sFar, _ := Score(far, "")

	if sNear <= sFar {
		t.Fatalf("with no preference, nearer unit (%.4f) must outrank farther (%.4f)", sNear, sFar)
	}
}

// TestScoredUnitSortIsDescending is a small guard on the ordering contract the
// service relies on: sorting ScoredUnits by Score descending puts the best first.
func TestScoredUnitSortIsDescending(t *testing.T) {
	units := []*models.Unit{
		unit(models.UnitTypeFire, 500),
		unit(models.UnitTypeAmbulance, 900),
		unit(models.UnitTypeRescue, 4000),
	}
	scored := make([]ScoredUnit, 0, len(units))
	for _, u := range units {
		s, bd := Score(u, models.UnitTypeAmbulance)
		scored = append(scored, ScoredUnit{Unit: *u, Score: s, Breakdown: bd})
	}
	sort.SliceStable(scored, func(i, j int) bool { return scored[i].Score > scored[j].Score })

	for i := 1; i < len(scored); i++ {
		if scored[i-1].Score < scored[i].Score {
			t.Fatalf("not sorted descending at %d: %.4f < %.4f", i, scored[i-1].Score, scored[i].Score)
		}
	}
}
