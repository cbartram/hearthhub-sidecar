package cmd

import (
	"context"
	"fmt"
	log "github.com/sirupsen/logrus"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"os"
	"path/filepath"
)

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
