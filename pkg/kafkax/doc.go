// Package kafkax wraps the Kafka client: producer construction, partition-key
// selection (aggregate_id, P20), and consumer-group setup. Isolates the broker
// library so modules depend on a small interface, not the vendor SDK.
package kafkax
