package relay

import (
	"context"
	"errors"
	"net"
	"strconv"

	"github.com/segmentio/kafka-go"
)

// EnsureTopic creates the topic if it does not already exist. Called at startup so
// the first publish doesn't race the broker's lazy topic creation (which can
// surface as "Unknown Topic Or Partition"). Idempotent: an already-exists response
// is treated as success.
func EnsureTopic(brokers []string, topic string, partitions int) error {
	conn, err := kafka.Dial("tcp", brokers[0])
	if err != nil {
		return err
	}
	defer conn.Close()

	controller, err := conn.Controller()
	if err != nil {
		return err
	}
	cc, err := kafka.Dial("tcp", net.JoinHostPort(controller.Host, strconv.Itoa(controller.Port)))
	if err != nil {
		return err
	}
	defer cc.Close()

	err = cc.CreateTopics(kafka.TopicConfig{Topic: topic, NumPartitions: partitions, ReplicationFactor: 1})
	if errors.Is(err, kafka.TopicAlreadyExists) {
		return nil
	}
	return err
}

// KafkaPublisher publishes events to a Kafka topic. RequireAll waits for all
// in-sync replicas to ack (durability); the Hash balancer routes by message key
// (aggregate id) so one aggregate's events keep to a single partition, preserving
// their order. AllowAutoTopicCreation so the topic appears on first write in dev.
type KafkaPublisher struct {
	w *kafka.Writer
}

func NewKafkaPublisher(brokers []string, topic string) *KafkaPublisher {
	return &KafkaPublisher{
		w: &kafka.Writer{
			Addr:                   kafka.TCP(brokers...),
			Topic:                  topic,
			Balancer:               &kafka.Hash{},
			RequiredAcks:           kafka.RequireAll,
			AllowAutoTopicCreation: true,
		},
	}
}

func (p *KafkaPublisher) Publish(ctx context.Context, key string, value []byte) error {
	return p.w.WriteMessages(ctx, kafka.Message{Key: []byte(key), Value: value})
}

func (p *KafkaPublisher) Close() error { return p.w.Close() }
