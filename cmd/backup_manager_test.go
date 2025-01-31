package cmd

import (
	"context"
	"errors"
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
		s3Client:        mockS3,
		backupFrequency: 100 * time.Millisecond,
		stopChan:        make(chan struct{}),
		sourceDir:       tmpDir,
		tenantDiscordId: "test-discord-id",
	}

	bm.Start()
	time.Sleep(150 * time.Millisecond)
	bm.GracefulShutdown()
	mockS3.AssertNumberOfCalls(t, "PutObject", 2) // One from ticker, one from shutdown
}
