package cmd

import (
	"bufio"
	"encoding/json"
	"fmt"
	"github.com/pkg/errors"
	amqp "github.com/rabbitmq/amqp091-go"
	log "github.com/sirupsen/logrus"
	"io"
	"os"
	"sync"
	"time"
)

type LogsCollector struct {
	rabbitMqManager     *RabbitMQManager
	tenantDiscordId     string
	stopChan            chan struct{}
	wg                  sync.WaitGroup
	lastCollectionMu    sync.Mutex
	lastCollectionTime  time.Time
	lastReadOffset      int64
	collectionFrequency time.Duration
}

func MakeLogsCollector(rabbit *RabbitMQManager, discordId string) (*LogsCollector, error) {
	return &LogsCollector{
		rabbitMqManager:     rabbit,
		tenantDiscordId:     discordId,
		wg:                  sync.WaitGroup{},
		collectionFrequency: time.Second * 3,
	}, nil
}

func (l *LogsCollector) StartCollection() {
	l.wg.Add(1)

	go func() {
		defer l.wg.Done()

		ticker := time.NewTicker(l.collectionFrequency)
		defer ticker.Stop()

		for {
			select {
			case <-l.stopChan:
				return
			case <-ticker.C:
				logs, err := l.getCurrentLogMessages()
				if err != nil {
					log.Errorf("error getting pod metrics: %v", err)
					continue
				}
				err = l.PublishLogs(logs)
				if err != nil {
					log.Errorf("error publishing metrics: %v", err)
				}
			}
		}
	}()

	log.Infof("go routine for metrics collection started")
}

func (l *LogsCollector) getCurrentLogMessages() ([]string, error) {
	file, err := os.Open("/valheim/BepInEx/config/server-logs.txt")
	if err != nil {
		return nil, errors.Wrap(err, "failed to open log file")
	}
	defer file.Close()

	// Seek to the last read position
	_, err = file.Seek(l.lastReadOffset, io.SeekStart)
	if err != nil {
		return nil, errors.Wrap(err, "failed to seek to last read position")
	}

	var newLines []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		newLines = append(newLines, scanner.Text())
	}

	if err := scanner.Err(); err != nil {
		return nil, errors.Wrap(err, "error while reading log file")
	}

	// Update the last read offset
	l.lastReadOffset, err = file.Seek(0, io.SeekCurrent) // Get the current position
	if err != nil {
		return nil, errors.Wrap(err, "failed to update last read offset")
	}

	return newLines, nil
}

func (l *LogsCollector) PublishLogs(logs []string) error {
	if len(logs) == 0 {
		return nil
	}

	logsJson, _ := json.Marshal(logs)
	message := &Message{
		Type:      "Logs",
		Body:      fmt.Sprintf(`{"containerName": "valheim-%s", "containerType": "server", "operation": "", "logs": %s}`, l.tenantDiscordId, logsJson),
		DiscordId: l.tenantDiscordId,
	}

	messageBytes, err := json.Marshal(message)
	if err != nil {
		log.Errorf("failed to publish log message: %v", err)
		return err
	}

	err = l.rabbitMqManager.Channel.Publish(
		"valheim-server-status", // exchange
		message.DiscordId,       // routing key
		false,
		false,
		amqp.Publishing{
			ContentType: "application/json",
			Body:        messageBytes,
		},
	)

	if err != nil {
		log.Errorf("error publishing metrics: %v", err)
		return errors.Wrap(err, "error publishing metrics")
	}

	return nil
}
