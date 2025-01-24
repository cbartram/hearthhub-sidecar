package main

import (
	"context"
	"fmt"
	log "github.com/sirupsen/logrus"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

const (
	sourceDir               = "/root/.config/unity3d/IronGate/Valheim"
	s3BucketName            = "hearthhub-backups"
	backupPrefix            = "valheim-backups-auto"
	gracefulShutdownTimeout = 1 * time.Minute
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

	log.Info("Starting Valheim server backup sidecar")

	backupManager, err := NewBackupManager()
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

// GetPodLabel retrieves a label from a pod given the label key. Returns "" if no label can be found or
// an error occurs.
func GetPodLabel(labelKey string) (string, error) {
	config, err := rest.InClusterConfig()
	if err != nil {
		return "", fmt.Errorf("error creating in-cluster config: %v", err)
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return "", fmt.Errorf("error creating Kubernetes client: %v", err)
	}

	podName := os.Getenv("HOSTNAME")
	pod, err := clientset.CoreV1().Pods("hearthhub").Get(context.Background(), podName, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("error retrieving pod: %v", err)
	}

	labelValue, exists := pod.Labels[labelKey]
	if !exists {
		return "", fmt.Errorf("label %s not found on pod", labelKey)
	}

	return labelValue, nil
}

// FindWorldFiles Recursively locates the *.fwl and *.db files on the PVC.
func FindWorldFiles(dir string) ([]string, error) {
	var worldFiles []string

	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if !info.IsDir() {
			ext := filepath.Ext(path)
			if ext == ".fwl" || ext == ".db" {
				worldFiles = append(worldFiles, path)
			}
		}

		return nil
	})

	if err != nil {
		return nil, err
	}

	log.Infof("found: %v world files", len(worldFiles))
	return worldFiles, nil
}
