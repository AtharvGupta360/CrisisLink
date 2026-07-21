// Package geo holds coordinate primitives shared by every geolocated domain
// (incidents, units, shelters, victims, transport).
//
// Before the modular split this validation lived in the incident service and the
// other three services imported ITS error sentinel — meaning shelter code depended
// on incident code for no reason other than a shared constant. That is exactly the
// kind of accidental coupling module boundaries are meant to remove, so the rule is
// applied once here and every module depends on this instead of on a sibling.
package geo

import "errors"

// ErrInvalidCoordinates is returned when a lat/lng pair is outside the valid range.
// Shared so every module returns the SAME sentinel and callers can errors.Is it
// regardless of which domain produced it.
var ErrInvalidCoordinates = errors.New("coordinates out of range")

// ValidateLatLng enforces the WGS-84 (SRID 4326) bounds we store points in.
//
// Note the asymmetry: latitude is ±90 (poles) and longitude is ±180 (antimeridian).
// Mixing these up is the classic geospatial bug — it silently accepts a swapped
// lat/lng pair, and PostGIS will happily store a point in the wrong hemisphere.
func ValidateLatLng(lat, lng float64) error {
	if lat < -90 || lat > 90 || lng < -180 || lng > 180 {
		return ErrInvalidCoordinates
	}
	return nil
}
