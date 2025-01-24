package main

import (
	"context"
	"fmt"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	log "github.com/sirupsen/logrus"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

const (
	sourceDir    = "/root/.config/unity3d/IronGate/Valheim"
	s3BucketName = "hearthhub-backups"
	backupPrefix = "valheim-backups-auto/"
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
	ctx := context.Background()

	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		log.Fatalf("unable to load AWS SDK config: %v", err)
	}

	backupFrequency := os.Getenv("BACKUP_FREQUENCY_MIN")
	var backupFrequencyDuration time.Duration

	if backupFrequency == "" {
		backupFrequencyDuration = 10 * time.Minute
	}

	backupFreqInt, err := strconv.Atoi(backupFrequency)
	if err != nil {
		log.Errorf("unable to parse BACKUP_FREQUENCY_MIN: %v defaulting to 10 minutes", err)
		backupFrequencyDuration = 10 * time.Minute
	} else {
		backupFrequencyDuration = time.Duration(backupFreqInt) * time.Minute
	}

	log.Infof("Backup frequency: %v", backupFrequencyDuration)
	s3Client := s3.NewFromConfig(cfg)
	ticker := time.NewTicker(backupFrequencyDuration)
	defer ticker.Stop()

	discordID, err := GetPodLabel("tenant-discord-id")
	if err != nil {
		log.Fatalf("Failed to get Discord ID: %v", err)
	}

	log.Infof("Tenant Discord ID: %s", discordID)

	// Perform an initial backup on server start.
	err = BackupWorldSaves(ctx, s3Client, discordID)
	if err != nil {
		log.Errorf("initial backup failed: %v", err)
	}

	for {
		select {
		case <-ticker.C:
			if err = BackupWorldSaves(ctx, s3Client, discordID); err != nil {
				log.Printf("backup failed: %v", err)
			}
		}
	}
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

// BackupWorldSaves Persists located *.fwl and *.db files to S3 periodically.
func BackupWorldSaves(ctx context.Context, s3Client *s3.Client, discordId string) error {
	files, err := FindWorldFiles(sourceDir)
	if err != nil {
		return fmt.Errorf("error finding world files: %v", err)
	}

	for _, file := range files {
		s3Key := fmt.Sprintf("%s/%s/%s", backupPrefix, discordId, filepath.Base(file))
		fileData, err := os.Open(file)
		if err != nil {
			log.Infof("Failed to open file %s: %v", file, err)
			continue
		}
		defer fileData.Close()

		// Upload to S3
		_, err = s3Client.PutObject(ctx, &s3.PutObjectInput{
			Bucket: aws.String(s3BucketName),
			Key:    aws.String(s3Key),
			Body:   fileData,
		})

		if err != nil {
			log.Printf("failed to upload backup %s to S3: %v", file, err)
			continue
		}

		log.Printf("successfully backed up %s to s3://%s/%s", file, s3BucketName, s3Key)
	}

	return nil
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
