package cmd

import (
	"context"
	"fmt"
	log "github.com/sirupsen/logrus"
	"gorm.io/gorm"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	metrics "k8s.io/metrics/pkg/client/clientset/versioned"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type ServiceWrapper struct {
	KubeClient     *kubernetes.Clientset
	MetricsClient  *metrics.Clientset
	RabbitMQClient *RabbitMQManager
	DB             *gorm.DB
}

type ValheimSaveFile struct {
	Path     string
	Name     string
	Size     int64
	IsBackup bool
}

// GetPodLabel retrieves a label from a pod given the label key. Returns "" if no label can be found or
// an error occurs.
func GetPodLabel(clientset kubernetes.Interface, labelKey string) (string, error) {
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

// CheckContainerStatus Checks each container within a pod to determine how many containers are ready. Returns true
// when all containers in a pod are ready and false otherwise.
func CheckContainerStatus(clientset kubernetes.Interface) bool {
	podName := os.Getenv("HOSTNAME")
	pod, err := clientset.CoreV1().Pods("hearthhub").Get(context.Background(), podName, metav1.GetOptions{})
	if err != nil {
		log.Errorf("error getting pod status: %v", err)
		return false
	}

	readyCount := 0
	expectedCount := len(pod.Status.ContainerStatuses)
	for _, containerStatus := range pod.Status.ContainerStatuses {
		if containerStatus.Ready {
			readyCount++
		}
	}

	log.Infof("%d/%d containers ready", readyCount, expectedCount)
	return readyCount == expectedCount
}

// GetWorldName Returns the name of the world for this server. This will be used in determining which backups to purge from s3. We want to make sure that
// if 5 backups get created for world-1 while world-0 has 3 backups that world-0's backups do not get purged just because world-1 has created additional backups.
// We shouldn't trust cognito for the world name because cognito attributes can be updated without the current server being restarted. i.e. server is running
// the arg: -world world-1 while cognito got updated with world-2 but the server never scaled/had anything installed to do a refresh.
func GetWorldName(clientset kubernetes.Interface, discordId string) (string, error) {
	deployment, err := clientset.AppsV1().Deployments("hearthhub").Get(context.Background(), fmt.Sprintf("valheim-%s", discordId), metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("error retrieving deployment: %v", err)
	}

	var args string
	for _, container := range deployment.Spec.Template.Spec.Containers {
		if container.Name == "valheim" {
			args = strings.Join(container.Args, " ")
			break
		}
	}

	worldName := ""
	parts := strings.Fields(args)
	for i, part := range parts {
		if part == "-world" && i+1 < len(parts) {
			worldName = parts[i+1]
			break
		}
	}

	if worldName == "" {
		return "", fmt.Errorf("world name not found in args")
	}

	return worldName, nil
}

// FindFiles Recursively locates the *.fwl and *.db files which are backups on the PVC.
func FindFiles(dir string) ([]ValheimSaveFile, error) {
	var worldFiles []ValheimSaveFile

	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if !info.IsDir() {
			ext := filepath.Ext(path)
			if ext == ".fwl" || ext == ".db" {
				worldFiles = append(worldFiles, ValheimSaveFile{
					Path:     path,
					Name:     info.Name(),
					Size:     info.Size(),
					IsBackup: strings.Contains(info.Name(), "_backup_auto-"),
				})
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
