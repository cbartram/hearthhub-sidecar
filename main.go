package main

import (
	"context"
	"flag"
	"fmt"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/cbartram/hearthhub-common/model"
	"github.com/cbartram/hearthhub-common/service"
	"github.com/cbartram/hearthhub-sidecar/cmd"
	log "github.com/sirupsen/logrus"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	metrics "k8s.io/metrics/pkg/client/clientset/versioned"
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

	kubeConfig, err := rest.InClusterConfig()
	if err != nil {
		log.Printf("could not create in cluster config. Attempting to load local kube config: %v", err.Error())
		kubeConfig, err = clientcmd.BuildConfigFromFlags("", clientcmd.RecommendedHomeFile)
		if err != nil {
			log.Fatalf("could not load local kubernetes config: %v", err.Error())
		}
		log.Printf("local kube config loaded successfully")
	}

	clientset, err := kubernetes.NewForConfig(kubeConfig)
	if err != nil {
		log.Errorf("error creating Kubernetes client: %v", err)
	}

	metricsClient, err := metrics.NewForConfig(kubeConfig)
	if err != nil {
		log.Errorf("error creating Metrics client: %v", err)
	}

	db := model.Connect()

	// Ensure only 1 instance of this exists for all go routines because they are all coming from the same ip (this pod).
	// This holds a reference to a channel to publish messages and all messages must be published on this channel.
	// Trying to open other channels will result in no message being sent and no errors being thrown.
	rabbit, err := cmd.MakeRabbitMQManager()
	if err != nil {
		log.Errorf("failed to make rabbitmq manager: %v", err)
		return
	}

	w := &cmd.ServiceWrapper{
		KubeClient:     clientset,
		MetricsClient:  metricsClient,
		RabbitMQClient: rabbit,
		DB:             db,
	}

	defer rabbit.Channel.Close()

	var mode, messageType, token string
	var maxBackups int
	flag.StringVar(&mode, "mode", "", "Sidecar Mode: backup or publish")
	flag.StringVar(&token, "token", "", "Tenant refresh token")
	flag.IntVar(&maxBackups, "max-backups", 3, "Maximum number of backups to keep in s3")
	flag.StringVar(&messageType, "type", "", "Message type: PostStart, PreStop, or Health")
	flag.Parse()

	if mode == "backup" {
		log.Infof("backup mode specified")
		StartBackups(w, token, maxBackups)
	} else if mode == "publish" {
		log.Infof("publish mode specified")
		Publish(clientset, rabbit, messageType)
	} else {
		log.Infof("no -mode specified defaulting to: backup")
		StartBackups(w, token, maxBackups)
	}

	log.Infof("closing rabbitmq channel")
}

// Publish Runs the aqmp publish protocol to send a message about the valheim server to the queue.
func Publish(clientset *kubernetes.Clientset, rabbit *cmd.RabbitMQManager, messageType string) {
	discordId, err := cmd.GetPodLabel(clientset, "tenant-discord-id")
	if err != nil {
		log.Errorf("failed to get pod label publish failed: %v", err)
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
func StartBackups(w *cmd.ServiceWrapper, token string, maxBackups int) {
	cfg, err := config.LoadDefaultConfig(context.Background())
	if err != nil {
		log.Fatalf("unable to load AWS SDK config: %v", err)
	}

	discordId, err := cmd.GetPodLabel(w.KubeClient, "tenant-discord-id")
	if err != nil {
		log.Fatalf("failed to get discord id from pod label: %v", err)
	}

	log.Info("Starting Valheim server backup sidecar")
	s3Client := cmd.MakeS3Client(cfg)
	cognito := service.MakeCognitoService(cfg)
	_, err = cognito.AuthUser(context.Background(), &token, &discordId, w.DB)
	if err != nil {
		log.Fatalf("error authenticating user, bad token or discord id mismatch: %v", err)
	}

	backupManager, err := cmd.NewBackupManager(w, s3Client, token, discordId, "/root/.config/unity3d/IronGate/Valheim/worlds_local", maxBackups)
	if err != nil {
		log.Fatalf("Failed to create backup manager: %v", err)
	}

	collector, err := cmd.MakeMetricsCollector(w, backupManager.TenantDiscordId)
	if err != nil {
		log.Errorf("failed to make metrics collector: %v", err)
		return
	}

	logCollector, err := cmd.MakeLogsCollector(w.RabbitMQClient, backupManager.TenantDiscordId)
	if err != nil {
		log.Errorf("failed to make log collector: %v", err)
		return
	}

	logCollector.StartCollection()
	collector.StartCollection()
	backupManager.Start()

	stopChan := make(chan os.Signal, 1)
	signal.Notify(stopChan, syscall.SIGINT, syscall.SIGTERM)

	<-stopChan

	backupManager.GracefulShutdown()

	log.Println("Valheim Backup sidecar shutting down")
}
