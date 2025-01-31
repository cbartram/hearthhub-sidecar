package main

import (
	"context"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/cbartram/hearthhub-sidecar/cmd"
	log "github.com/sirupsen/logrus"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"os"
	"os/signal"
	"syscall"
)

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

	cfg, err := config.LoadDefaultConfig(context.Background())
	if err != nil {
		log.Fatalf("unable to load AWS SDK config: %v", err)
	}

	config, err := rest.InClusterConfig()
	if err != nil {
		log.Fatalf("error creating in-cluster config: %v", err)
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		log.Fatalf("error creating Kubernetes client: %v", err)
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
