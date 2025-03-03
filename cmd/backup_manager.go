package cmd

import (
	"context"
	"errors"
	"fmt"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/cbartram/hearthhub-common/model"
	log "github.com/sirupsen/logrus"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	backupPrefix            = "valheim-backups-auto"
	gracefulShutdownTimeout = 1 * time.Minute
	serverLogsPath          = "/valheim/BepInEx/config/server-logs.txt"
)

type BackupFile struct {
	Key      string
	DateTime time.Time
	Type     string // "fwl" or "db"
	BaseName string // name without extension and timestamp
}

type BackupPair struct {
	FWL BackupFile
	DB  BackupFile
}
type BackupManager struct {
	s3Client          ObjectStore
	wrapper           *ServiceWrapper
	token             string
	stopChan          chan struct{}
	stopPodStatusChan chan struct{}
	sourceDir         string
	wg                sync.WaitGroup
	lastBackupMu      sync.Mutex
	lastBackupTime    time.Time
	backupFrequency   time.Duration
	TenantDiscordId   string
	maxBackups        int
	lastCleanupMu     sync.Mutex
	lastCleanupTime   time.Time
	cleanupFrequency  time.Duration
}

func NewBackupManager(w *ServiceWrapper, s3Client *S3Client, token, discordId, sourceDir string, maxBackups int) (*BackupManager, error) {
	backupFrequency := os.Getenv("BACKUP_FREQUENCY_MIN")
	var backupFrequencyDuration, cleanupFrequencyDuration time.Duration

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

	log.Infof("tenant id: %s, backups occur every: %v, cleanups occur every: %v", discordId, backupFrequencyDuration, cleanupFrequencyDuration)

	return &BackupManager{
		s3Client:          s3Client,
		wrapper:           w,
		token:             token,
		sourceDir:         sourceDir,
		stopChan:          make(chan struct{}),
		stopPodStatusChan: make(chan struct{}),
		lastBackupTime:    time.Time{},
		backupFrequency:   backupFrequencyDuration,
		TenantDiscordId:   discordId,
		lastCleanupTime:   time.Time{},
		cleanupFrequency:  backupFrequencyDuration,
		maxBackups:        maxBackups,
	}, nil
}

// BackupWorldSaves Persists located *.fwl and *.db files to S3 periodically. This will back up
// all worlds in the pvc not just the worlds for the running server. The cleanup process is responsible
// for ensuring backups get purged from disk and S3.
func (bm *BackupManager) BackupWorldSaves(ctx context.Context) error {
	files, err := FindFiles(bm.sourceDir)
	if err != nil {
		return fmt.Errorf("error finding world files: %v", err)
	}

	var user model.User
	tx := bm.wrapper.DB.Where("discord_id = ?", bm.TenantDiscordId).First(&user)
	if tx.Error != nil {
		log.Errorf("failed to fetch user from db: %v", tx.Error)
		return err
	}
	// TODO Handle multiple servers here
	serverId := user.Servers[0].ID
	user.BackupFiles = []model.BackupFile{}
	user.WorldFiles = []model.WorldFile{}

	for _, file := range files {
		fileName := filepath.Base(file)
		s3Key := fmt.Sprintf("%s/%s/%s", backupPrefix, bm.TenantDiscordId, fileName)
		fileData, err := os.Open(file)
		if err != nil {
			log.Infof("Failed to open file %s: %v", file, err)
			continue
		}
		defer fileData.Close()

		if strings.HasSuffix(fileName, ".db") {
			if strings.Contains(fileName, "_backup_auto-") {
				user.BackupFiles = append(user.BackupFiles, model.BackupFile{
					BaseFile: model.BaseFile{
						FileName:  fileName,
						Installed: true,
						S3Key:     s3Key,
					},
				})
			} else {
				user.WorldFiles = append(user.WorldFiles, model.WorldFile{
					BaseFile: model.BaseFile{
						FileName:  fileName,
						Installed: true,
						S3Key:     s3Key,
					},
					ServerID: serverId,
				})
			}
		}

		_, err = bm.s3Client.PutObject(ctx, &s3.PutObjectInput{
			Bucket: aws.String(os.Getenv("BUCKET_NAME")),
			Key:    aws.String(s3Key),
			Body:   fileData,
		})

		if err != nil {
			// In the event 1 file fails to upload we fail the rest of them.
			log.Errorf("failed to upload backup %s to S3: %v", file, err)
			return err
		}

		log.Infof("successfully backed up %s to s3://%s/%s", file, os.Getenv("BUCKET_NAME"), s3Key)
	}

	bm.wrapper.DB.Save(&user)
	return nil
}

// PerformPeriodicBackup Performs the backup of S3 files and subsequently cleans up any old
// backups from both s3 and the pvc
func (bm *BackupManager) PerformPeriodicBackup() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	bm.lastBackupMu.Lock()
	bm.lastBackupTime = time.Now()
	bm.lastBackupMu.Unlock()

	log.Infof("performing periodic s3 BACKUP at: %s", time.Now().Format(time.RFC3339))
	if err := bm.BackupWorldSaves(ctx); err != nil {
		log.Errorf("periodic backup failed: %v", err)
	}

	log.Infof("performing periodic s3 CLEANUP at: %s", time.Now().Format(time.RFC3339))
	if err := bm.Cleanup(ctx); err != nil {
		log.Errorf("periodic cleanup failed: %v", err)
	}
}

// Cleanup Cleans the automatically generated backups by finding all world files for a players server in S3,
// sorting by the timestamps of the backups, and deleting all but the most recent n backups
func (bm *BackupManager) Cleanup(ctx context.Context) error {
	worldName, err := GetWorldName(bm.wrapper.KubeClient, bm.TenantDiscordId)
	if err != nil {
		log.Errorf("failed to get world name, skipping delete: %v", err)
		return err
	}

	log.Infof("running cleanup process for the last: %d backups for world: %s", bm.maxBackups, worldName)

	result, err := bm.s3Client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
		Bucket: aws.String(os.Getenv("BUCKET_NAME")),
		Prefix: aws.String(fmt.Sprintf("%s/%s/", backupPrefix, bm.TenantDiscordId)),
	})

	if err != nil {
		log.Errorf("failed to list objects in bucket %s: %v", os.Getenv("BUCKET_NAME"), err)
		return err
	}

	fwlMap := make(map[string]BackupFile)
	dbMap := make(map[string]BackupFile)
	filesToDelete := make([]string, 0)

	// First pass: categorize all files
	for _, obj := range result.Contents {
		filename := filepath.Base(*obj.Key)

		// Skip if not a backup file
		if !strings.Contains(filename, "_backup_auto-") || !strings.Contains(filename, worldName) {
			log.Infof("skipping file (not backup or does not match world: %s, file: %s", worldName, filename)
			continue
		}

		// Extract the base name and timestamp
		parts := strings.Split(filename, "_backup_auto-")
		if len(parts) != 2 {
			log.Errorf("unexected number of parts from spliting file: %s, len: %v", filename, len(parts))
			continue
		}

		baseName := parts[0]
		timestampWithExt := parts[1]

		// Trim off the .db or .fwl extension
		timestamp := strings.Split(timestampWithExt, ".")[0]

		// Parse the datetime
		dt, err := time.Parse("20060102150405", timestamp)
		if err != nil {
			log.Errorf("failed to parse timestamp from file: %s, error: %v", filename, err)
			continue
		}

		backupFile := BackupFile{
			Key:      *obj.Key,
			DateTime: dt,
			BaseName: baseName,
		}

		if strings.HasSuffix(filename, ".fwl") {
			backupFile.Type = "fwl"
			fwlMap[timestamp] = backupFile
		} else if strings.HasSuffix(filename, ".db") {
			backupFile.Type = "db"
			dbMap[timestamp] = backupFile
		}
	}

	var pairs []BackupPair

	// Find orphaned .fwl files (no matching .db)
	for timestamp, fwl := range fwlMap {
		db, exists := dbMap[timestamp]
		if !exists {
			filesToDelete = append(filesToDelete, fwl.Key)
			log.Infof("found orphaned file: %s", fwl.Key)
		} else {
			// Create pairs of matching .fwl and .db files
			pairs = append(pairs, BackupPair{FWL: fwl, DB: db})
			log.Infof("found backup pair: fwl: %s db: %s", fwl.Key, db.Key)
		}
	}

	// Sort pairs by timestamp (oldest first)
	sort.Slice(pairs, func(i, j int) bool {
		return pairs[i].FWL.DateTime.Before(pairs[j].FWL.DateTime)
	})

	// Mark older pairs for deletion, keeping only the newest pairs
	if len(pairs) > bm.maxBackups {
		pairsToDelete := pairs[:len(pairs)-bm.maxBackups]
		for _, pair := range pairsToDelete {
			log.Infof("scheduling .db/.fwl pair: %s for deletion", pair.DB.Key)
			filesToDelete = append(filesToDelete, pair.FWL.Key)
			filesToDelete = append(filesToDelete, pair.DB.Key)
		}
	} else {
		log.Infof("backups: %d do not exceed configured max backups: %d", len(pairs), bm.maxBackups)
	}

	// Delete the oldest n files
	for _, file := range filesToDelete {
		deleteInput := &s3.DeleteObjectInput{
			Bucket: aws.String(os.Getenv("BUCKET_NAME")),
			Key:    &file,
		}

		_, err := bm.s3Client.DeleteObject(context.TODO(), deleteInput)
		if err != nil {
			return errors.New(fmt.Sprintf("failed to delete object %s: %v", file, err))
		}

		// Also make sure to delete this file from the pvc or it will just get re-uploaded to s3
		localFile := fmt.Sprintf("%s/%s", bm.sourceDir, filepath.Base(file))
		os.Remove(localFile)

		log.Infof("deleted s3 file: %s and local file: %s", file, localFile)
	}

	var user model.User
	tx := bm.wrapper.DB.Where("discord_id = ?", bm.TenantDiscordId).First(&user)
	if tx.Error != nil {
		log.Errorf("failed to get user: %v", tx.Error)
		return err
	}

	serverId := user.Servers[0].ID
	user.BackupFiles = []model.BackupFile{}
	user.WorldFiles = []model.WorldFile{}

	files, _ := FindFiles(bm.sourceDir)
	for _, file := range files {
		filename := filepath.Base(file)

		if !strings.Contains(filename, worldName) || !strings.HasSuffix(filename, ".db") {
			continue
		}

		s3Key := fmt.Sprintf("%s/%s/%s", backupPrefix, bm.TenantDiscordId, filename)
		if strings.Contains(filename, "_backup_auto-") {
			user.BackupFiles = append(user.BackupFiles, model.BackupFile{
				BaseFile: model.BaseFile{
					FileName:  filename,
					Installed: true,
					S3Key:     s3Key,
				},
			})
		} else if filename == (worldName + ".db") {
			user.WorldFiles = append(user.WorldFiles, model.WorldFile{
				BaseFile: model.BaseFile{
					FileName:  filename,
					Installed: true,
					S3Key:     s3Key,
				},
				ServerID: serverId,
			})
		}
	}

	bm.wrapper.DB.Save(&user)
	return nil
}

// Start Starts a ticker based on the configured backup frequency to perform backups of world files
// to s3 at every tick and start a separate go routine to perform cleanups of those backups every tick. Both
// go routines share the same channel so that they stop together when the container (server) stops.
func (bm *BackupManager) Start() {
	log.Infof("starting backup manager go-routines")
	bm.wg.Add(3)

	// Backup Goroutine
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

	// Pod container status ticker. Checks the current pod to see when both containers are ready
	// When that event occurs the ticker stops and a message is published to a rabbitMQ queue.
	go func() {
		defer bm.wg.Done()

		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-bm.stopPodStatusChan:
				return
			case <-ticker.C:
				ready := CheckContainerStatus(bm.wrapper.KubeClient)
				if ready {
					content, err := os.ReadFile(serverLogsPath)
					if err != nil {
						log.Errorf("failed to read server logs: %v", err)
					}
					code := parseJoinCode(string(content))

					log.Infof("container ready with join code: %s", code)

					// Notifies the frontend about the join code
					err = bm.wrapper.RabbitMQClient.PublishMessage(&Message{
						Type:      "JoinCode",
						Body:      fmt.Sprintf(`{"joinCode": "%s", "containerName": "valheim-%s", "containerType": "server", "operation": ""}`, code, bm.TenantDiscordId),
						DiscordId: bm.TenantDiscordId,
					})

					if err != nil {
						log.Errorf("failed to publish join code message: %v", err)
					}

					// Notifies the frontend that it can move the server status to ready
					err = bm.wrapper.RabbitMQClient.PublishMessage(&Message{
						Type:      "ContainerReady",
						Body:      fmt.Sprintf(`{"containerName": "valheim-%s", "containerType": "server", "operation": ""}`, bm.TenantDiscordId),
						DiscordId: bm.TenantDiscordId,
					})

					if err != nil {
						log.Errorf("failed to publish pod container status ready message: %v", err)
					}

					log.Infof("sent rabbitmq messages")
					close(bm.stopPodStatusChan)
				}
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
		log.Errorf("final backup failed: %v", err)
	}

	if err := bm.Cleanup(ctx); err != nil {
		log.Errorf("final cleanup failed: %v", err)
	}
}
