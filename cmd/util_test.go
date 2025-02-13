package cmd

import (
	"context"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/api/core/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGetPodLabel(t *testing.T) {
	// Save original hostname and restore after test
	originalHostname := os.Getenv("HOSTNAME")
	defer os.Setenv("HOSTNAME", originalHostname)

	// Set test hostname
	testPodName := "test-pod"
	os.Setenv("HOSTNAME", testPodName)

	tests := []struct {
		name          string
		labelKey      string
		podLabels     map[string]string
		expected      string
		expectedError bool
	}{
		{
			name:     "successful label retrieval",
			labelKey: "app",
			podLabels: map[string]string{
				"app": "test-app",
			},
			expected:      "test-app",
			expectedError: false,
		},
		{
			name:          "label doesn't exist",
			labelKey:      "nonexistent",
			podLabels:     map[string]string{},
			expected:      "",
			expectedError: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Create fake clientset
			clientset := fake.NewClientset(&v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      testPodName,
					Namespace: "hearthhub",
					Labels:    tc.podLabels,
				},
			})

			result, err := GetPodLabel(clientset, tc.labelKey)

			if tc.expectedError && err == nil {
				t.Error("expected error but got none")
			}
			if !tc.expectedError && err != nil {
				t.Errorf("unexpected error: %v", err)
			}

			if result != tc.expected {
				t.Errorf("expected %q but got %q", tc.expected, result)
			}
		})
	}
}

func TestFindWorldFiles(t *testing.T) {
	// Create temporary test directory
	tmpDir, err := os.MkdirTemp("", "test-worlds-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	testFiles := map[string]bool{
		"world1.fwl":        true,  // should be found
		"world2.db":         true,  // should be found
		"other.txt":         false, // should not be found
		"subdir/world3.fwl": true,  // should be found
	}

	for filename, _ := range testFiles {
		filePath := filepath.Join(tmpDir, filename)
		dir := filepath.Dir(filePath)
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("failed to create directory %s: %v", dir, err)
		}
		if err := os.WriteFile(filePath, []byte("test"), 0644); err != nil {
			t.Fatalf("failed to create file %s: %v", filename, err)
		}
	}

	files, err := FindWorldFiles(tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expectedCount := 0
	for _, shouldFind := range testFiles {
		if shouldFind {
			expectedCount++
		}
	}
	if len(files) != expectedCount {
		t.Errorf("expected %d files but found %d", expectedCount, len(files))
	}

	// Check each found file
	for _, file := range files {
		base := filepath.Base(file)
		ext := filepath.Ext(base)
		if ext != ".fwl" && ext != ".db" {
			t.Errorf("found unexpected file type: %s", file)
		}
	}
}

func TestFindWorldFilesErrors(t *testing.T) {
	// Test with non-existent directory
	_, err := FindWorldFiles("/nonexistent/directory")
	if err == nil {
		t.Error("expected error for non-existent directory but got none")
	}

	// Test with invalid permissions (if running as non-root)
	if os.Getuid() != 0 {
		tmpDir, err := os.MkdirTemp("", "test-noperm-*")
		if err != nil {
			t.Fatalf("failed to create temp dir: %v", err)
		}
		defer os.RemoveAll(tmpDir)

		// Remove all permissions
		if err := os.Chmod(tmpDir, 0000); err != nil {
			t.Fatalf("failed to change permissions: %v", err)
		}

		_, err = FindWorldFiles(tmpDir)
		if err == nil {
			t.Error("expected error for no-permission directory but got none")
		}

		// Restore permissions so cleanup can succeed
		os.Chmod(tmpDir, 0755)
	}
}

func TestGetWorldName(t *testing.T) {
	tests := []struct {
		name        string
		discordId   string
		deployment  *appsv1.Deployment
		wantWorld   string
		wantErr     bool
		errContains string
	}{
		{
			name:      "successful retrieval",
			discordId: "123456",
			deployment: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "valheim-123456",
					Namespace: "hearthhub",
				},
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name: "valheim",
									Args: []string{"./valheim_server.x86_64 -name foo -port 2456 -world MyWorld -port 2456 -password bar"},
								},
							},
						},
					},
				},
			},
			wantWorld: "MyWorld",
			wantErr:   false,
		},
		{
			name:      "deployment not found",
			discordId: "nonexistent",
			deployment: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "valheim-different",
					Namespace: "hearthhub",
				},
			},
			wantWorld:   "",
			wantErr:     true,
			errContains: "error retrieving deployment",
		},
		{
			name:      "valheim container not found",
			discordId: "789012",
			deployment: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "valheim-789012",
					Namespace: "hearthhub",
				},
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name: "different-container",
									Args: []string{"./valheim_server.x86_64 -name foo -port 2456 -world MyWorld -port 2456 -password bar"},
								},
							},
						},
					},
				},
			},
			wantWorld: "",
			wantErr:   true,
		},
		{
			name:      "world flag not found",
			discordId: "345678",
			deployment: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "valheim-345678",
					Namespace: "hearthhub",
				},
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name: "valheim",
									Args: []string{"-port 2456"},
								},
							},
						},
					},
				},
			},
			wantWorld: "",
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a fake clientset and add the test deployment
			clientset := fake.NewClientset()
			if tt.deployment != nil {
				_, err := clientset.AppsV1().Deployments("hearthhub").Create(
					context.Background(),
					tt.deployment,
					metav1.CreateOptions{},
				)
				if err != nil {
					t.Fatalf("error creating test deployment: %v", err)
				}
			}

			// Call the function being tested
			gotWorld, err := GetWorldName(clientset, tt.discordId)

			// Check error cases
			if tt.wantErr {
				if err == nil {
					t.Error("GetWorldName() expected error but got none")
				} else if tt.errContains != "" && !contains(err.Error(), tt.errContains) {
					t.Errorf("GetWorldName() error = %v, want error containing %v", err, tt.errContains)
				}
				return
			}
			if err != nil {
				t.Errorf("GetWorldName() unexpected error: %v", err)
				return
			}

			// Check the returned world name
			if gotWorld != tt.wantWorld {
				t.Errorf("GetWorldName() = %v, want %v", gotWorld, tt.wantWorld)
			}
		})
	}
}

// Helper function to check if a string contains another string
func contains(s, substr string) bool {
	return strings.Contains(s, substr)
}
