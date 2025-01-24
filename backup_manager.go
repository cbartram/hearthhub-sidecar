package main

import (
	"context"
	"fmt"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	log "github.com/sirupsen/logrus"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

type BackupManager struct {
	s3Client        *s3.Client
	stopChan        chan struct{}
	wg              sync.WaitGroup
	lastBackupMu    sync.Mutex
	lastBackupTime  time.Time
	backupFrequency time.Duration
	tenantDiscordId string
}

func NewBackupManager() (*BackupManager, error) {
	ctx := context.Background()
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to load AWS SDK config: %v", err)
	}

	s3Client := s3.NewFromConfig(cfg)

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

	log.Infof("backups occur every: %v", backupFrequencyDuration)

	discordID, err := GetPodLabel("tenant-discord-id")
	if err != nil {
		log.Fatalf("Failed to get Discord ID: %v", err)
	}

	log.Infof("tenant Discord ID: %s", discordID)

	return &BackupManager{
		s3Client:        s3Client,
		stopChan:        make(chan struct{}),
		lastBackupTime:  time.Time{},
		backupFrequency: backupFrequencyDuration,
		tenantDiscordId: discordID,
	}, nil
}

// BackupWorldSaves Persists located *.fwl and *.db files to S3 periodically.
func (bm *BackupManager) BackupWorldSaves(ctx context.Context) error {
	files, err := FindWorldFiles(sourceDir)
	if err != nil {
		return fmt.Errorf("error finding world files: %v", err)
	}

	for _, file := range files {
		s3Key := fmt.Sprintf("%s/%s/%s", backupPrefix, bm.tenantDiscordId, filepath.Base(file))
		fileData, err := os.Open(file)
		if err != nil {
			log.Infof("Failed to open file %s: %v", file, err)
			continue
		}
		defer fileData.Close()

		_, err = bm.s3Client.PutObject(ctx, &s3.PutObjectInput{
			Bucket: aws.String(s3BucketName),
			Key:    aws.String(s3Key),
			Body:   fileData,
		})

		if err != nil {
			log.Infof("failed to upload backup %s to S3: %v", file, err)
			continue
		}

		log.Infof("successfully backed up %s to s3://%s/%s", file, s3BucketName, s3Key)
	}

	return nil
}

// PerformPeriodicBackup Performs the backup of S3 files
func (bm *BackupManager) PerformPeriodicBackup() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	bm.lastBackupMu.Lock()
	bm.lastBackupTime = time.Now()
	bm.lastBackupMu.Unlock()

	if err := bm.BackupWorldSaves(ctx); err != nil {
		log.Errorf("periodic backup failed: %v", err)
	}
}

// Start Starts a ticker based on the configured backup frequency to perform backups of world files
// to s3 at every tick.
func (bm *BackupManager) Start() {
	bm.wg.Add(1)

	go func() {
		defer bm.wg.Done()

		ticker := time.NewTicker(bm.backupFrequency)
		defer ticker.Stop()

		for {
			select {
			case <-bm.stopChan:
				return
			case <-ticker.C:
				bm.PerformPeriodicBackup()
			}
		}
	}()
}

// GracefulShutdown Performs a final backup before the container stops.
func (bm *BackupManager) GracefulShutdown() {
	close(bm.stopChan)
	bm.wg.Wait()

	ctx, cancel := context.WithTimeout(context.Background(), gracefulShutdownTimeout)
	defer cancel()

	log.Println("Performing final backup before shutdown")
	if err := bm.BackupWorldSaves(ctx); err != nil {
		log.Printf("Final backup failed: %v", err)
	}
}
