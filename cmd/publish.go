package cmd

import (
	"encoding/json"
	"fmt"
	amqp "github.com/rabbitmq/amqp091-go"
	log "github.com/sirupsen/logrus"
	"os"
)

type RabbitMQManager struct {
	Channel *amqp.Channel
}

func MakeRabbitMQManager() (*RabbitMQManager, error) {
	credentials := fmt.Sprintf("%s:%s", os.Getenv("RABBITMQ_DEFAULT_USER"), os.Getenv("RABBITMQ_DEFAULT_PASS"))
	conn, err := amqp.Dial(fmt.Sprintf("amqp://%s@%s/", credentials, os.Getenv("RABBITMQ_BASE_URL")))
	if err != nil {
		log.Errorf("failed to connect to RabbitMQ: %v", err)
		return nil, err
	}

	ch, err := conn.Channel()
	if err != nil {
		log.Errorf("failed to open a channel: %v", err)
		return nil, err
	}

	return &RabbitMQManager{
		Channel: ch,
	}, nil
}

type Message struct {
	Type      string `json:"type"`
	Body      string `json:"content"` // It is expected that Body is pre-encoded json string i.e. "{\"content\": \"foo\"}"
	DiscordId string `json:"discord_id"`
}

func (rm *RabbitMQManager) PublishMessage(message *Message) error {
	messageBytes, err := json.Marshal(message)
	if err != nil {
		log.Errorf("failed to publish message: %v", err)
		return err
	}

	defer rm.Channel.Close()

	return rm.Channel.Publish(
		"valheim-server-status", // exchange
		"#",                     // routing key
		false,                   // mandatory
		false,                   // immediate
		amqp.Publishing{
			ContentType: "application/json",
			Body:        messageBytes,
		},
	)
}
