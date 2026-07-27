import { MapContainer, TileLayer, CircleMarker, Tooltip, useMap } from 'react-leaflet';
import { useEffect } from 'react';

// CircleMarker rather than Leaflet's default Marker on purpose: the default icon
// is loaded from an image path that breaks under bundlers (the classic
// "marker-icon.png 404"), and a circle lets colour carry meaning — severity for
// incidents, availability for units.

export const SEVERITY_COLOR = {
  critical: '#e5534b',
  high: '#db8c3a',
  medium: '#c9b03c',
  low: '#6b7887',
};

export const UNIT_COLOR = {
  available: '#3fb950',
  reserved: '#4c8dd6',
  dispatched: '#4c8dd6',
  out_of_service: '#4a5462',
};

// Recenter imperatively when the selection changes. react-leaflet gives no
// declarative "fly here" prop, so this tiny child component reaches for the map
// instance via context.
function FlyTo({ center, zoom }) {
  const map = useMap();
  useEffect(() => {
    if (center) map.flyTo(center, zoom ?? map.getZoom(), { duration: 0.6 });
  }, [center?.[0], center?.[1]]); // eslint-disable-line react-hooks/exhaustive-deps
  return null;
}

export default function MapView({
  center = [28.6139, 77.209],
  zoom = 12,
  incidents = [],
  units = [],
  selectedId,
  onSelectIncident,
  flyTo,
  children,
}) {
  return (
    <MapContainer center={center} zoom={zoom} className="leaflet-container" preferCanvas>
      {/* CARTO dark tiles so the map matches the console rather than fighting it. */}
      <TileLayer
        url="https://{s}.basemaps.cartocdn.com/dark_all/{z}/{x}/{y}{r}.png"
        attribution='&copy; OpenStreetMap &copy; CARTO'
      />
      <FlyTo center={flyTo} />

      {incidents.map((i) => {
        const selected = i.id === selectedId;
        return (
          <CircleMarker
            key={i.id}
            center={[i.latitude, i.longitude]}
            radius={selected ? 13 : 9}
            pathOptions={{
              color: SEVERITY_COLOR[i.severity] || '#6b7887',
              fillColor: SEVERITY_COLOR[i.severity] || '#6b7887',
              fillOpacity: selected ? 0.75 : 0.45,
              weight: selected ? 3 : 2,
            }}
            eventHandlers={{ click: () => onSelectIncident?.(i) }}
          >
            <Tooltip direction="top">
              <b>{i.title}</b>
              <br />
              {i.severity} · {i.status}
              {i.reportCount > 1 && <> · {i.reportCount} reports</>}
            </Tooltip>
          </CircleMarker>
        );
      })}

      {units.map((u) => (
        <CircleMarker
          key={u.id}
          center={[u.latitude, u.longitude]}
          radius={6}
          pathOptions={{
            color: u.isLive === false ? '#4a5462' : UNIT_COLOR[u.status] || '#6b7887',
            fillColor: u.isLive === false ? 'transparent' : UNIT_COLOR[u.status] || '#6b7887',
            fillOpacity: 0.9,
            weight: 2,
            // A dashed outline with no fill reads instantly as "this unit has gone
            // dark" — its presence key expired.
            dashArray: u.isLive === false ? '3 3' : undefined,
          }}
        >
          <Tooltip direction="top">
            <b>{u.callSign}</b>
            <br />
            {u.type} · {u.status}
            {u.isLive === false && <> · no heartbeat</>}
          </Tooltip>
        </CircleMarker>
      ))}

      {children}
    </MapContainer>
  );
}
