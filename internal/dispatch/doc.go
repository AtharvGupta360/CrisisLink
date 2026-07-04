// Package dispatch is the core: the KNN candidate query, the pure explainable
// scoring function, and the SELECT ... FOR UPDATE reservation transaction that
// makes double-booking a rescue unit impossible under concurrency (P11–P15).
// Sole owner of the `dispatches` table. This is the package we design so it
// could later be extracted into its own service; its reservation transaction is
// deliberately kept local so it never has to cross a service boundary.
package dispatch
