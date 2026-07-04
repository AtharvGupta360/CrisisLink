// Package incident owns citizen-reported incidents: creation, the status state
// machine, the stored geometry point, spatial radius search, and near-duplicate
// dedupe (P7–P9). Sole owner of the `incidents` table. Built out from P7 onward.
package incident
