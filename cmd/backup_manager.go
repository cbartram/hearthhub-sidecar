package cmd

import (
	"context"
	"errors"
	"fmt"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	log "github.com/sirupsen/logrus"
	"k8s.io/client-go/kubernetes"
	"os"
	"path/filepath"
	"regexp"
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
	kubeClient        kubernetes.Interface
	cognito           CognitoService
	rabbitMQManager   *RabbitMQManager
	token             string
	stopChan          chan struct{}
	stopPodStatusChan chan struct{}
	sourceDir         string
	wg                sync.WaitGroup
	lastBackupMu      sync.Mutex
	lastBackupTime    time.Time
	backupFrequency   time.Duration
	TenantDiscordId   string
	lastCleanupMu     sync.Mutex
	lastCleanupTime   time.Time
	cleanupFrequency  time.Duration
}

func NewBackupManager(s3Client *S3Client, clientset kubernetes.Interface, cognito CognitoService, rabbit *RabbitMQManager, token, sourceDir string) (*BackupManager, error) {
	backupFrequency := os.Getenv("BACKUP_FREQUENCY_MIN")
	cleanupFrequency := os.Getenv("CLEANUP_FREQUENCY_MIN")
	var backupFrequencyDuration, cleanupFrequencyDuration time.Duration

	if backupFrequency == "" {
		backupFrequencyDuration = 10 * time.Minute
	}

	if cleanupFrequency == "" {
		cleanupFrequencyDuration = 1 * time.Hour
	}

	cleanupFreqInt, err := strconv.Atoi(cleanupFrequency)
	if err != nil {
		log.Errorf("unable to parse CLEANUP_FREQUENCY_MIN: %v defaulting to 60 minutes", err)
		cleanupFrequencyDuration = 1 * time.Hour
	} else {
		cleanupFrequencyDuration = time.Duration(cleanupFreqInt) * time.Minute
	}

	backupFreqInt, err := strconv.Atoi(backupFrequency)
	if err != nil {
		log.Errorf("unable to parse BACKUP_FREQUENCY_MIN: %v defaulting to 10 minutes", err)
		backupFrequencyDuration = 10 * time.Minute
	} else {
		backupFrequencyDuration = time.Duration(backupFreqInt) * time.Minute
	}

	discordID, err := GetPodLabel(clientset, "tenant-discord-id")
	if err != nil {
		log.Fatalf("Failed to get Discord ID: %v", err)
	}

	log.Infof("tenant id: %s, backups occur every: %v, cleanups occur every: %v", discordID, backupFrequencyDuration, cleanupFrequencyDuration)

	return &BackupManager{
		s3Client:          s3Client,
		kubeClient:        clientset,
		cognito:           cognito,
		token:             token,
		sourceDir:         sourceDir,
		rabbitMQManager:   rabbit,
		stopChan:          make(chan struct{}),
		stopPodStatusChan: make(chan struct{}),
		lastBackupTime:    time.Time{},
		backupFrequency:   backupFrequencyDuration,
		TenantDiscordId:   discordID,
		lastCleanupTime:   time.Time{},
		cleanupFrequency:  backupFrequencyDuration,
	}, nil
}

// BackupWorldSaves Persists located *.fwl and *.db files to S3 periodically.
func (bm *BackupManager) BackupWorldSaves(ctx context.Context) error {
	files, err := FindWorldFiles(bm.sourceDir)
	if err != nil {
		return fmt.Errorf("error finding world files: %v", err)
	}

	user, err := bm.cognito.AuthUser(ctx, &bm.token, &bm.TenantDiscordId)
	if err != nil {
		return fmt.Errorf("error authenticating user: %v", err)
	}
	var dbFiles []string
	for _, file := range files {
		s3Key := fmt.Sprintf("%s/%s/%s", backupPrefix, bm.TenantDiscordId, filepath.Base(file))
		fileData, err := os.Open(file)
		if err != nil {
			log.Infof("Failed to open file %s: %v", file, err)
			continue
		}
		defer fileData.Close()

		if strings.HasSuffix(filepath.Base(file), ".db") {
			dbFiles = append(dbFiles, filepath.Base(file))
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

	// Finally update cognito with the fact that the user now has n files backed up from their pvc. i.e. the files
	// are already installed on the pvc
	log.Infof("merging: %d synced s3 backup files with cognito user attributes", len(dbFiles))
	err = bm.cognito.MergeInstalledFilesBatch(ctx, user, dbFiles)
	if err != nil {
		log.Errorf("failed to merge installed files: %v", err)
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

// PerformPeriodicCleanup Performs a periodic cleanup of the automated backups in S3 by remove all but the
// last n backups for a given world.
func (bm *BackupManager) PerformPeriodicCleanup() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	bm.lastCleanupMu.Lock()
	bm.lastCleanupTime = time.Now()
	bm.lastCleanupMu.Unlock()

	if err := bm.Cleanup(ctx); err != nil {
		log.Errorf("periodic cleanup failed: %v", err)
	}
}

// Cleanup Cleans the automatically generated backups by finding all world files for a players server in S3,
// sorting by the timestamps of the backups, and deleting all but the most recent n backups
func (bm *BackupManager) Cleanup(ctx context.Context) error {
	maxBackups := os.Getenv("MAX_BACKUPS")
	maxBackupsInt, err := strconv.Atoi(maxBackups)
	if err != nil {
		log.Errorf("unable to parse MAX_BACKUPS: %v defaulting to 3", err)
		maxBackupsInt = 3
	}

	worldName, err := GetWorldName(bm.kubeClient, bm.TenantDiscordId)
	if err != nil {
		log.Errorf("failed to get world name, skipping delete: %v", err)
		return err
	}

	log.Infof("running cleanup process for the last: %d backups for world: %s", maxBackupsInt, worldName)

	result, err := bm.s3Client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
		Bucket: aws.String(os.Getenv("BUCKET_NAME")),
		Prefix: aws.String(fmt.Sprintf("%s/%s/", backupPrefix, bm.TenantDiscordId)),
	})

	if err != nil {
		log.Errorf("failed to list objects in bucket %s: %v", os.Getenv("BUCKET_NAME"), err)
		return err
	}

	fwlMap := make(map[string]BackupFile) // map[timestamp]BackupFile
	dbMap := make(map[string]BackupFile)  // map[timestamp]BackupFile
	filesToDelete := make([]string, 0)

	// First pass: categorize all files
	for _, obj := range result.Contents {
		filename := filepath.Base(*obj.Key)

		// Skip if not a backup file
		if !strings.Contains(filename, "_backup_auto-") || !strings.Contains(filename, worldName) {
			log.Infof("skipping (either not backup or not matching world as running server): %s", filename)
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
	if len(pairs) > maxBackupsInt {
		pairsToDelete := pairs[:len(pairs)-maxBackupsInt]
		for _, pair := range pairsToDelete {
			log.Infof("scheduling .db/.fwl pair: %s for deletion", pair.FWL.BaseName)
			filesToDelete = append(filesToDelete, pair.FWL.Key)
			filesToDelete = append(filesToDelete, pair.DB.Key)
		}
	} else {
		log.Infof("backups: %d do not exceed configured max backups: %d", len(pairs), maxBackupsInt)
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
		log.Infof("deleted: %s", file)
	}
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
				log.Infof("performing periodic s3 backup")
				bm.PerformPeriodicBackup()
			}
		}
	}()

	// Cleanup goroutine
	go func() {
		defer bm.wg.Done()

		cleanupTicker := time.NewTicker(bm.cleanupFrequency)
		defer cleanupTicker.Stop()

		for {
			select {
			case <-bm.stopChan:
				return
			case <-cleanupTicker.C:
				log.Infof("performing periodic s3 cleanup")
				bm.PerformPeriodicCleanup()
			}
		}
	}()

	// Pod container status ticker. Checks the current pod to see when both containers are ready
	// When that event occurs the ticker stops and a message is published to a rabbitMQ queue.
	go func() {
		defer bm.wg.Done()

		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-bm.stopPodStatusChan:
				return
			case <-ticker.C:
				ready := CheckContainerStatus(bm.kubeClient)
				if ready {
					content, err := os.ReadFile(serverLogsPath)
					if err != nil {
						log.Errorf("failed to read server logs: %v", err)
					}
					code := parseJoinCode(string(content))

					log.Infof("container ready with join code: %s", code)

					// Notifies the frontend about the join code
					err = bm.rabbitMQManager.PublishMessage(&Message{
						Type:      "JoinCode",
						Body:      fmt.Sprintf(`{"joinCode": "%s", "containerName": "valheim-%s", "containerType": "server", "operation": ""}`, code, bm.TenantDiscordId),
						DiscordId: bm.TenantDiscordId,
					})

					if err != nil {
						log.Errorf("failed to publish join code message: %v", err)
					}

					// Notifies the frontend that it can move the server status to ready
					err = bm.rabbitMQManager.PublishMessage(&Message{
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

func parseJoinCode(input string) string {
	pattern := `join code (\d+)`
	re := regexp.MustCompile(pattern)
	match := re.FindStringSubmatch(input)

	if len(match) > 1 {
		// Return the captured group (the numbers after "join code")
		return strings.TrimSpace(match[1])
	}

	return "not-found"
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
