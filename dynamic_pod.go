package main

import (
	"context"
	"fmt"
	"os"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8s_intstr "k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

type StreamConfig struct {
	Name      string `json:"Name"`
	StreamID  string `json:"StreamID"`
	VideoURLs string `json:"VideoURLs"`
	Namespace string `json:"Namespace"`
	OutputURL string `json:"OutputURL"` // ← যোগ করো
	OutputID  string `json:"OutputID"`  // ← যোগ করো
}

type K8sClient struct {
	client *kubernetes.Clientset
}

func NewK8sClient() (*K8sClient, error) {
	var config *rest.Config
	var err error

	// k3s pod এর ভেতরে থাকলে InClusterConfig
	config, err = rest.InClusterConfig()
	if err != nil {
		// local test এর জন্য fallback
		kubeconfig := os.Getenv("KUBECONFIG")
		if kubeconfig == "" {
			kubeconfig = "/etc/rancher/k3s/k3s.yaml"
		}
		config, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
		if err != nil {
			return nil, fmt.Errorf("config error: %w", err)
		}
	}

	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("client error: %w", err)
	}

	return &K8sClient{client: client}, nil
}

func (k *K8sClient) StartStream(cfg StreamConfig) error {
	if cfg.Namespace == "" {
		cfg.Namespace = "default"
	}

	// আগে আছে কিনা চেক
	_, err := k.client.AppsV1().Deployments(cfg.Namespace).Get(
		context.Background(), cfg.Name, metav1.GetOptions{},
	)
	if err == nil {
		return fmt.Errorf("stream '%s' already running", cfg.Name)
	}

	replicas := int32(1)

	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cfg.Name,
			Namespace: cfg.Namespace,
			Labels: map[string]string{
				"app":  cfg.Name,
				"type": "stream-server",
			},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": cfg.Name},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"app": cfg.Name},
				},
				Spec: corev1.PodSpec{
					HostNetwork:                   true,
					DNSPolicy:                     corev1.DNSClusterFirstWithHostNet,
					TerminationGracePeriodSeconds: int64Ptr(0),
					Containers: []corev1.Container{
						{
							Name:            cfg.Name,
							Image:           "gostreampro:latest",
							ImagePullPolicy: corev1.PullNever,
							Ports: []corev1.ContainerPort{
								{ContainerPort: 8080},
							},
							Env: []corev1.EnvVar{
								{
									Name:  "STREAM_ID",
									Value: cfg.StreamID,
								},
								{
									Name:  "VIDEO_URLS",
									Value: cfg.VideoURLs,
								},
								{
									Name:  "DEFAULT_OUTPUT_URL",
									Value: cfg.OutputURL,
								},
								{
									Name:  "DEFAULT_OUTPUT_ID",
									Value: cfg.OutputID,
								},
								{
									Name: "API_KEY",
									ValueFrom: &corev1.EnvVarSource{
										SecretKeyRef: &corev1.SecretKeySelector{
											LocalObjectReference: corev1.LocalObjectReference{
												Name: "stream-secret",
											},
											Key: "api-key",
										},
									},
								},
							},
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceMemory: resource.MustParse("256Mi"),
									corev1.ResourceCPU:    resource.MustParse("250m"),
								},
								Limits: corev1.ResourceList{
									corev1.ResourceMemory: resource.MustParse("2Gi"),
									corev1.ResourceCPU:    resource.MustParse("2000m"),
								},
							},
							LivenessProbe: &corev1.Probe{
								ProbeHandler: corev1.ProbeHandler{
									HTTPGet: &corev1.HTTPGetAction{
										Path: "/health",
										Port: k8s_intstr.FromInt(8080),
									},
								},
								InitialDelaySeconds: 15,
								PeriodSeconds:       30,
							},
							ReadinessProbe: &corev1.Probe{
								ProbeHandler: corev1.ProbeHandler{
									HTTPGet: &corev1.HTTPGetAction{
										Path: "/health",
										Port: k8s_intstr.FromInt(8080),
									},
								},
								InitialDelaySeconds: 10,
								PeriodSeconds:       10,
							},
						},
					},
				},
			},
		},
	}

	_, err = k.client.AppsV1().Deployments(cfg.Namespace).Create(
		context.Background(), deployment, metav1.CreateOptions{},
	)
	if err != nil {
		return fmt.Errorf("deployment create error: %w", err)
	}

	fmt.Printf("✅ Stream '%s' started\n", cfg.Name)
	return nil
}

func (k *K8sClient) StopStream(name, namespace string) error {
	if namespace == "" {
		namespace = "default"
	}

	err := k.client.AppsV1().Deployments(namespace).Delete(
		context.Background(), name, metav1.DeleteOptions{},
	)
	if errors.IsNotFound(err) {
		return fmt.Errorf("stream '%s' not found", name)
	}
	if err != nil {
		return fmt.Errorf("deployment delete error: %w", err)
	}

	fmt.Printf("🛑 Stream '%s' stopped\n", name)
	return nil
}

func (k *K8sClient) ListStreams(namespace string) ([]map[string]string, error) {
	if namespace == "" {
		namespace = "default"
	}

	list, err := k.client.AppsV1().Deployments(namespace).List(
		context.Background(),
		metav1.ListOptions{
			LabelSelector: "type=stream-server",
		},
	)
	if err != nil {
		return nil, fmt.Errorf("list error: %w", err)
	}

	var streams []map[string]string
	for _, d := range list.Items {
		status := "Running"
		if d.Status.ReadyReplicas == 0 {
			status = "Pending"
		}
		streams = append(streams, map[string]string{
			"name":   d.Name,
			"ready":  fmt.Sprintf("%d/%d", d.Status.ReadyReplicas, d.Status.Replicas),
			"status": status,
		})
	}

	return streams, nil
}

func int64Ptr(i int64) *int64 { return &i }
