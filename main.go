package main

import (
	"context"
	"flag"
	"fmt"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/cbartram/hearthhub-sidecar/cmd"
	log "github.com/sirupsen/logrus"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"os"
	"os/signal"
	"syscall"
)

// main This sidecar has 2 functions:
// - Persisting and cleaning up backups of world saves to s3
// - Publishing messages about server (pod) status
// Functions are determined at container startup using the `-mode` flag set to either "backup" or "publish".
// By default the backup functionality is used.
func main() {
	logger := log.New()
	logger.SetFormatter(&log.TextFormatter{
		FullTimestamp: false,
	})

	logLevel, err := log.ParseLevel(os.Getenv("LOG_LEVEL"))
	if err != nil {
		logLevel = log.InfoLevel
	}

	log.SetOutput(os.Stdout)
	log.SetLevel(logLevel)

	config, err := rest.InClusterConfig()
	if err != nil {
		log.Printf("could not create in cluster config. Attempting to load local kube config: %v", err.Error())
		config, err = clientcmd.BuildConfigFromFlags("", clientcmd.RecommendedHomeFile)
		if err != nil {
			log.Fatalf("could not load local kubernetes config: %v", err.Error())
		}
		log.Printf("local kube config loaded successfully")
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		log.Errorf("error creating Kubernetes client: %v", err)
	}

	var mode, messageType string
	flag.StringVar(&mode, "mode", "", "Sidecar Mode: backup or publish")
	flag.StringVar(&messageType, "type", "", "Message type: PostStart, PreStop, or Health")
	flag.Parse()

	if mode == "backup" {
		log.Infof("backup mode specified")
		StartBackups(clientset)
	} else if mode == "publish" {
		log.Infof("publish mode specified")
		Publish(clientset, messageType)
	} else {
		log.Infof("no -mode specified defaulting to: backup")
		StartBackups(clientset)
	}

}

// Publish Runs the aqmp publish protocol to send a message about the valheim server to the queue.
func Publish(clientset *kubernetes.Clientset, messageType string) {
	discordId, err := cmd.GetPodLabel(clientset, "tenant-discord-id")
	if err != nil {
		log.Errorf("failed to get pod label publish failed: %v", err)
		return
	}

	rabbit, err := cmd.MakeRabbitMQManager()
	if err != nil {
		log.Errorf("failed to make rabbitmq manager: %v", err)
		return
	}

	if messageType != "PostStart" && messageType != "PreStop" {
		log.Errorf("invalid message type: %s", messageType)
		return
	}

	// "operation" here is simply included to keep consistency with file-manager rabbitmq messages
	// it doesn't contain any context about what event occurred for this message.
	message := &cmd.Message{
		Type:      messageType,
		Body:      fmt.Sprintf(`{"containerName": "valheim-%s", "containerType": "server", "operation": ""}`, discordId),
		DiscordId: discordId,
	}
	err = rabbit.PublishMessage(message)
	if err != nil {
		log.Errorf("failed to publish message: %v", err)
	}
	log.Infof("%s message published successfully", messageType)
}

// StartBackups Starts the backup process to S3.
func StartBackups(clientset *kubernetes.Clientset) {
	cfg, err := config.LoadDefaultConfig(context.Background())
	if err != nil {
		log.Fatalf("unable to load AWS SDK config: %v", err)
	}

	log.Info("Starting Valheim server backup sidecar")

	s3Client := cmd.MakeS3Client(cfg)
	backupManager, err := cmd.NewBackupManager(s3Client, clientset, "/root/.config/unity3d/IronGate/Valheim")
	if err != nil {
		log.Fatalf("Failed to create backup manager: %v", err)
	}

	// Start periodic backups
	backupManager.Start()

	// Set up signal handling for graceful shutdown
	stopChan := make(chan os.Signal, 1)
	signal.Notify(stopChan, syscall.SIGINT, syscall.SIGTERM)

	// Wait for shutdown signal
	<-stopChan

	// Perform final backup and cleanup
	backupManager.GracefulShutdown()

	log.Println("Valheim Backup sidecar shutting down")
}
