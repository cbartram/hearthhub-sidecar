package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/pkg/errors"
	amqp "github.com/rabbitmq/amqp091-go"
	log "github.com/sirupsen/logrus"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	metrics "k8s.io/metrics/pkg/client/clientset/versioned"
	"os"
	"sync"
	"time"
)

// PodMetrics contains the CPU and memory usage for a pod
type PodMetrics struct {
	CPU struct {
		CurrentMillis int64
		LimitMillis   int64
		RequestMillis int64
		UsagePercent  float64
	}
	Memory struct {
		CurrentBytes int64
		LimitBytes   int64
		RequestBytes int64
		UsagePercent float64
	}
}

type MetricsCollector struct {
	rabbitMqManager     *RabbitMQManager
	metricsClient       metrics.Clientset
	tenantDiscordId     string
	kubeClient          kubernetes.Interface
	stopChan            chan struct{}
	wg                  sync.WaitGroup
	lastCollectionMu    sync.Mutex
	lastCollectionTime  time.Time
	collectionFrequency time.Duration
}

func MakeMetricsCollector(kubeClient kubernetes.Interface, metricsClient *metrics.Clientset, rabbit *RabbitMQManager, discordId string) (*MetricsCollector, error) {
	return &MetricsCollector{
		rabbitMqManager:     rabbit,
		kubeClient:          kubeClient,
		tenantDiscordId:     discordId,
		metricsClient:       *metricsClient,
		wg:                  sync.WaitGroup{},
		collectionFrequency: time.Second * 10,
	}, nil
}

func (m *MetricsCollector) StartCollection() {
	m.wg.Add(1)

	// Metrics collection Goroutine
	go func() {
		defer m.wg.Done()

		ticker := time.NewTicker(m.collectionFrequency)
		defer ticker.Stop()

		for {
			select {
			case <-m.stopChan:
				return
			case <-ticker.C:
				collectedMetrics, err := m.getCurrentPodMetrics(context.Background())
				if err != nil {
					log.Errorf("error getting pod metrics: %v", err)
					continue
				}
				err = m.PublishMetrics(collectedMetrics)
				if err != nil {
					log.Errorf("error publishing metrics: %v", err)
				}
			}
		}
	}()

	log.Infof("go routine for metrics collection started")
}

func (m *MetricsCollector) PublishMetrics(metrics *PodMetrics) error {
	message := &Message{
		Type: "Metrics",
		Body: fmt.Sprintf(
			`{"containerName": "valheim-%s", "containerType": "server", "operation": "", "cpuUtilization": "%v", "memoryUtilization": "%v"}`,
			m.tenantDiscordId, metrics.CPU.UsagePercent, metrics.Memory.UsagePercent),
		DiscordId: m.tenantDiscordId,
	}

	messageBytes, err := json.Marshal(message)
	if err != nil {
		log.Errorf("failed to publish message: %v", err)
		return err
	}

	err = m.rabbitMqManager.Channel.Publish(
		"valheim-server-status", // exchange
		message.DiscordId,       // routing key
		false,
		false,
		amqp.Publishing{
			ContentType: "application/json",
			Body:        messageBytes,
		},
	)

	if err != nil {
		log.Errorf("error publishing metrics: %v", err)
		return errors.Wrap(err, "error publishing metrics")
	}

	return nil
}

// GetCurrentPodMetrics retrieves the current CPU and memory metrics for the pod this code is running in
func (m *MetricsCollector) getCurrentPodMetrics(ctx context.Context) (*PodMetrics, error) {
	podName := os.Getenv("POD_NAME")
	if podName == "" {
		var err error
		podName, err = os.Hostname()
		if err != nil {
			return nil, fmt.Errorf("failed to get pod name: %v", err)
		}
	}

	namespace := os.Getenv("POD_NAMESPACE")
	if namespace == "" {
		data, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace")
		if err == nil {
			namespace = string(data)
		} else {
			return nil, fmt.Errorf("failed to get namespace: %v", err)
		}
	}

	pod, err := m.kubeClient.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get pod spec: %v", err)
	}

	podMetrics, err := m.metricsClient.MetricsV1beta1().PodMetricses(namespace).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get pod metrics: %v", err)
	}

	result := &PodMetrics{}

	// Calculate total current usage
	for _, container := range podMetrics.Containers {
		result.CPU.CurrentMillis += container.Usage.Cpu().MilliValue()
		result.Memory.CurrentBytes += container.Usage.Memory().Value()
	}

	// Calculate total limits and requests
	for _, container := range pod.Spec.Containers {
		limits := container.Resources.Limits
		requests := container.Resources.Requests

		if cpu := limits.Cpu(); cpu != nil {
			result.CPU.LimitMillis += cpu.MilliValue()
		}
		if memory := limits.Memory(); memory != nil {
			result.Memory.LimitBytes += memory.Value()
		}
		if cpu := requests.Cpu(); cpu != nil {
			result.CPU.RequestMillis += cpu.MilliValue()
		}
		if memory := requests.Memory(); memory != nil {
			result.Memory.RequestBytes += memory.Value()
		}
	}

	// Calculate percentages
	// If limit is set, calculate against limit, otherwise use request
	if result.CPU.LimitMillis > 0 {
		result.CPU.UsagePercent = float64(result.CPU.CurrentMillis) / float64(result.CPU.LimitMillis) * 100
	} else if result.CPU.RequestMillis > 0 {
		result.CPU.UsagePercent = float64(result.CPU.CurrentMillis) / float64(result.CPU.RequestMillis) * 100
	}

	if result.Memory.LimitBytes > 0 {
		result.Memory.UsagePercent = float64(result.Memory.CurrentBytes) / float64(result.Memory.LimitBytes) * 100
	} else if result.Memory.RequestBytes > 0 {
		result.Memory.UsagePercent = float64(result.Memory.CurrentBytes) / float64(result.Memory.RequestBytes) * 100
	}

	return result, nil
}

// formatMetrics returns a human-readable string of the metrics
func formatMetrics(m *PodMetrics) string {
	return fmt.Sprintf(
		"CPU: %.1f%% (%dm/%dm limit, %dm request) Memory: %.1f%% (%dMi/%dMi limit, %dMi request)",
		m.CPU.UsagePercent,
		m.CPU.CurrentMillis,
		m.CPU.LimitMillis,
		m.CPU.RequestMillis,
		m.Memory.UsagePercent,
		m.Memory.CurrentBytes/(1024*1024),
		m.Memory.LimitBytes/(1024*1024),
		m.Memory.RequestBytes/(1024*1024),
	)
}
