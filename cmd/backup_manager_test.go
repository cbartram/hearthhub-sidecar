package cmd

import (
	"context"
	"errors"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go/ptr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// MockObjectStore implements ObjectStore interface for testing
type MockObjectStore struct {
	mock.Mock
}

func (m *MockObjectStore) PutObject(ctx context.Context, params *s3.PutObjectInput, optFns ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	args := m.Called(ctx, params)
	return args.Get(0).(*s3.PutObjectOutput), args.Error(1)
}

func (m *MockObjectStore) DeleteObject(ctx context.Context, params *s3.DeleteObjectInput, optFns ...func(*s3.Options)) (*s3.DeleteObjectOutput, error) {
	args := m.Called(ctx, params)
	return args.Get(0).(*s3.DeleteObjectOutput), args.Error(1)
}

func (m *MockObjectStore) ListObjectsV2(ctx context.Context, params *s3.ListObjectsV2Input, optFns ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	args := m.Called(ctx, params)
	return args.Get(0).(*s3.ListObjectsV2Output), args.Error(1)
}

func TestNewBackupManager(t *testing.T) {
	tests := []struct {
		name          string
		backupFreqEnv string
		expectedFreq  time.Duration
		discordID     string
		setupMocks    func(clientset kubernetes.Interface)
		expectedError bool
	}{
		{
			name:          "Default backup frequency",
			backupFreqEnv: "",
			expectedFreq:  10 * time.Minute,
			discordID:     "discord-id-foo",
			setupMocks: func(mk kubernetes.Interface) {
				// Setup mock to return discord ID
			},
			expectedError: false,
		},
		{
			name:          "Custom backup frequency",
			backupFreqEnv: "15",
			expectedFreq:  15 * time.Minute,
			discordID:     "discord-id-foo",
			setupMocks: func(mk kubernetes.Interface) {
				// Setup mock to return discord ID
			},
			expectedError: false,
		},
		{
			name:          "Invalid backup frequency",
			backupFreqEnv: "invalid",
			expectedFreq:  10 * time.Minute,
			discordID:     "discord-id-foo",
			setupMocks: func(mk kubernetes.Interface) {
				// Setup mock to return discord ID
			},
			expectedError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup
			os.Setenv("BACKUP_FREQUENCY_MIN", tt.backupFreqEnv)
			os.Setenv("HOSTNAME", "test-pod")
			defer os.Unsetenv("BACKUP_FREQUENCY_MIN")

			mockK8s := fake.NewClientset(&corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: "hearthhub",
					Labels: map[string]string{
						"tenant-discord-id": "discord-id-foo",
					},
				},
			})
			tt.setupMocks(mockK8s)

			tmpDir := t.TempDir()

			bm, err := NewBackupManager(&S3Client{}, mockK8s, tmpDir)
			if tt.expectedError {
				assert.Error(t, err)
				assert.Nil(t, bm)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, bm)
				assert.Equal(t, tt.expectedFreq, bm.backupFrequency)
				assert.Equal(t, tt.discordID, bm.tenantDiscordId)
			}
		})
	}
}

func TestBackupWorldSaves(t *testing.T) {
	tests := []struct {
		name          string
		files         []string
		setupMocks    func(*MockObjectStore)
		expectedError bool
	}{
		{
			name:  "Successful backup",
			files: []string{"world.fwl", "world.db"},
			setupMocks: func(m *MockObjectStore) {
				m.On("PutObject", mock.Anything, mock.Anything).
					Return(&s3.PutObjectOutput{}, nil)
			},
			expectedError: false,
		},
		{
			name:  "S3 upload failure",
			files: []string{"world.fwl"},
			setupMocks: func(m *MockObjectStore) {
				m.On("PutObject", mock.Anything, mock.Anything).
					Return(&s3.PutObjectOutput{}, errors.New("invalid bucket name"))
			},
			expectedError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockS3 := &MockObjectStore{}
			tt.setupMocks(mockS3)

			tmpDir := t.TempDir()
			for _, file := range tt.files {
				path := filepath.Join(tmpDir, file)
				err := os.WriteFile(path, []byte("test data"), 0644)
				assert.NoError(t, err)
			}

			bm := &BackupManager{
				s3Client:        mockS3,
				tenantDiscordId: "test-discord-id",
				sourceDir:       tmpDir,
			}

			err := bm.BackupWorldSaves(context.Background())

			if tt.expectedError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
			mockS3.AssertExpectations(t)
		})
	}
}

func TestPerformPeriodicBackup(t *testing.T) {
	// Setup
	mockS3 := &MockObjectStore{}
	mockS3.On("PutObject", mock.Anything, mock.Anything).
		Return(&s3.PutObjectOutput{}, nil)

	tmpDir := t.TempDir()
	for _, file := range []string{"world.fwl", "world.db"} {
		path := filepath.Join(tmpDir, file)
		err := os.WriteFile(path, []byte("test data"), 0644)
		assert.NoError(t, err)
	}

	bm := &BackupManager{
		s3Client:        mockS3,
		tenantDiscordId: "test-discord-id",
		sourceDir:       tmpDir,
	}

	bm.PerformPeriodicBackup()

	assert.False(t, bm.lastBackupTime.IsZero())
	mockS3.AssertExpectations(t)
}

func TestPerformPeriodicCleanup(t *testing.T) {
	// Setup
	mockS3 := &MockObjectStore{}
	mockS3.On("ListObjectsV2", mock.Anything, mock.Anything).
		Return(&s3.ListObjectsV2Output{
			Contents: []types.Object{
				{Key: ptr.String("runewraiths-test-world-3_backup_auto-20250201122344.fwl")},
				{Key: ptr.String("runewraiths-test-world-3_backup_auto-20250201122344.db")},
				{Key: ptr.String("runewraiths-test-world-3_backup_auto-20250201122345.fwl")},
				{Key: ptr.String("runewraiths-test-world-3_backup_auto-20250201122345.db")},
				{Key: ptr.String("runewraiths-test-world-3_backup_auto-20250201122346.fwl")},
				{Key: ptr.String("runewraiths-test-world-3_backup_auto-20250201122346.db")},
				{Key: ptr.String("runewraiths-test-world-3_backup_auto-20250201122347.fwl")},
				{Key: ptr.String("runewraiths-test-world-3_backup_auto-20250201122347.db")},
				// These worlds will be skipped by the cleanup for this server. Even though they are the oldest backups they may
				// be the newest backups for their respective world and thus shouldn't be deleted
				{Key: ptr.String("different-world_backup_auto-20250201122341.db")},
				{Key: ptr.String("different-world_backup_auto-20250201122341.fwl")},
			},
		}, nil)
	mockS3.On("DeleteObject", mock.Anything, mock.Anything).Return(&s3.DeleteObjectOutput{}, nil)

	tmpDir := t.TempDir()
	for _, file := range []string{"runewraiths-test-world-3_backup_auto-20250201122344.fwl", "runewraiths-test-world-3_backup_auto-20250201122344.fwl"} {
		path := filepath.Join(tmpDir, file)
		err := os.WriteFile(path, []byte("test data"), 0644)
		assert.NoError(t, err)
	}

	bm := &BackupManager{
		s3Client:        mockS3,
		tenantDiscordId: "test-discord-id",
		sourceDir:       tmpDir,
		kubeClient: fake.NewClientset(&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "valheim-test-discord-id",
				Namespace: "hearthhub",
			},
			Spec: appsv1.DeploymentSpec{
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name: "valheim",
								Args: []string{"./valheim_server.x86_64", "-name", "foo", "-port", "2456", "-world", "runewraiths-test-world-3", "-port", "2456", "-password", "bar"},
							},
						},
					},
				},
			},
		}),
	}

	bm.PerformPeriodicCleanup()

	assert.False(t, bm.lastCleanupTime.IsZero())
	mockS3.AssertExpectations(t)
}

func TestCleanupOrphans(t *testing.T) {
	mockS3 := &MockObjectStore{}
	mockS3.On("ListObjectsV2", mock.Anything, mock.Anything).
		Return(&s3.ListObjectsV2Output{
			Contents: []types.Object{
				{Key: ptr.String("runewraiths-test-world-3_backup_auto-20250201122343.fwl")}, // Old orphaned file
				{Key: ptr.String("runewraiths-test-world-3_backup_auto-20250201122344.fwl")},
				{Key: ptr.String("runewraiths-test-world-3_backup_auto-20250201122344.db")},
				{Key: ptr.String("runewraiths-test-world-3_backup_auto-20250201122345.fwl")},
				{Key: ptr.String("runewraiths-test-world-3_backup_auto-20250201122345.db")},
				{Key: ptr.String("runewraiths-test-world-3_backup_auto-20250201122346.fwl")},
				{Key: ptr.String("runewraiths-test-world-3_backup_auto-20250201122346.db")},
				{Key: ptr.String("runewraiths-test-world-3_backup_auto-20250201122347.fwl")},
				{Key: ptr.String("runewraiths-test-world-3_backup_auto-20250201122347.db")},
				{Key: ptr.String("runewraiths-test-world-3_backup_auto-20250201122348.fwl")}, // New orphaned file
			},
		}, nil)
	mockS3.On("DeleteObject", mock.Anything, mock.Anything).Return(&s3.DeleteObjectOutput{}, nil)

	bm := &BackupManager{
		s3Client:        mockS3,
		tenantDiscordId: "test-discord-id",
		kubeClient: fake.NewClientset(&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "valheim-test-discord-id",
				Namespace: "hearthhub",
			},
			Spec: appsv1.DeploymentSpec{
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name: "valheim",
								Args: []string{"./valheim_server.x86_64", "-name", "foo", "-port", "2456", "-world", "runewraiths-test-world-3", "-port", "2456", "-password", "bar"},
							},
						},
					},
				},
			},
		}),
	}

	err := bm.Cleanup(context.Background())

	assert.NoError(t, err)
	mockS3.AssertExpectations(t)
}

func TestStart(t *testing.T) {
	mockS3 := &MockObjectStore{}
	mockS3.On("PutObject", mock.Anything, mock.Anything).
		Return(&s3.PutObjectOutput{}, nil)

	tmpDir := t.TempDir()
	for _, file := range []string{"world.fwl"} {
		path := filepath.Join(tmpDir, file)
		err := os.WriteFile(path, []byte("test data"), 0644)
		assert.NoError(t, err)
	}

	bm := &BackupManager{
		s3Client:         mockS3,
		backupFrequency:  100 * time.Millisecond,
		stopChan:         make(chan struct{}),
		sourceDir:        tmpDir,
		tenantDiscordId:  "test-discord-id",
		cleanupFrequency: 100 * time.Minute,
	}

	bm.Start()
	time.Sleep(150 * time.Millisecond)
	bm.GracefulShutdown()
	mockS3.AssertNumberOfCalls(t, "PutObject", 2) // One from ticker, one from shutdown
}
