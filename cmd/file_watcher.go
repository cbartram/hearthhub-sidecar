package cmd

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/fsnotify/fsnotify"
	amqp "github.com/rabbitmq/amqp091-go"
	log "github.com/sirupsen/logrus"
	"io"
	"os"
	"strings"
)

type FileWatcher struct {
	RabbitMQManager *RabbitMQManager
}

func MakeFileWatcher() (*FileWatcher, error) {
	rabbit, err := MakeRabbitMQManager()
	if err != nil {
		log.Errorf("failed to make RabbitMQ manager for file watcher: %v", err)
		return nil, err
	}

	return &FileWatcher{RabbitMQManager: rabbit}, nil
}

func (f *FileWatcher) Start(filename, discordId string) {
	joinCodes := make(chan string)

	// Start monitoring in a separate goroutine
	go func() {
		err := f.watchFile(filename, joinCodes)
		if err != nil {
			log.Errorf("failed to watch file: %v", err)
		}
	}()

	log.Infof("started monitoring for join codes...")

	go func() {
		for code := range joinCodes {
			log.Infof("found join code: %s, publishing to discord id: %s", code, discordId)
			msg := Message{
				Type:      "JoinCode",
				Body:      fmt.Sprintf(`{"joinCode": "%s"}`, code),
				DiscordId: discordId,
			}
			messageBytes, err := json.Marshal(msg)
			if err != nil {
				log.Errorf("failed to marshall join code message: %v", err)
			}

			err = f.RabbitMQManager.Channel.Publish(
				"valheim-server-status", // exchange
				msg.DiscordId,           // routing key
				false,
				false,
				amqp.Publishing{
					ContentType: "application/json",
					Body:        messageBytes,
				},
			)

			if err != nil {
				log.Errorf("failed to publish join code message: %v", err)
			}
		}
	}()
}
func (f *FileWatcher) watchFile(path string, joinCodes chan<- string) error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Errorf("error creating watcher: %v", err)
		return err
	}
	defer watcher.Close()

	// Open the file and seek to end
	file, err := os.Open(path)
	if err != nil {
		log.Errorf("error opening file: %v", err)
		return err
	}
	defer file.Close()

	file.Seek(0, io.SeekEnd)
	scanner := bufio.NewScanner(file)

	if err := watcher.Add(path); err != nil {
		log.Errorf("error adding watcher: %v", err)
		return err
	}

	for {
		select {
		case event, ok := <-watcher.Events:
			if !ok {
				return errors.New("file watcher closed unexpectedly")
			}

			if event.Op&fsnotify.Write == fsnotify.Write {
				for scanner.Scan() {
					line := scanner.Text()
					if strings.Contains(line, "Join Code") {
						joinCodes <- line
					}
				}
			}
		case err, ok := <-watcher.Errors:
			if !ok {
				return errors.New(fmt.Sprintf("file watcher received error event: %v", err))
			}
		}
	}
}
