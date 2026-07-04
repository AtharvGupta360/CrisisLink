// Package shelter owns shelters and their bed inventory, including the
// FOR UPDATE bed-reservation transaction that prevents over-allocating beds
// (P16–P17). Sole owner of the `shelters` table. Built out from P16 onward.
package shelter
