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
	OutputURL string `json:"OutputURL"`
	OutputID  string `json:"OutputID"`
}

// StartStream এর return value — assigned port সহ
type StreamInfo struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Port      int32  `json:"port"`       // K8s assigned NodePort
	AccessURL string `json:"access_url"` // http://NodeIP:Port
}

type K8sClient struct {
	client *kubernetes.Clientset
	nodeIP string // cluster এর Node IP (একবার set করলেই হয়)
}

func NewK8sClient() (*K8sClient, error) {
	var config *rest.Config
	var err error

	config, err = rest.InClusterConfig()
	if err != nil {
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

	k := &K8sClient{client: client}

	// Node IP auto-detect করুন
	k.nodeIP, err = k.getNodeIP()
	if err != nil {
		// fallback: env থেকে নিন
		k.nodeIP = os.Getenv("NODE_IP")
		if k.nodeIP == "" {
			return nil, fmt.Errorf("node IP পাওয়া যায়নি: %w", err)
		}
	}

	return k, nil
}

// Cluster এর যেকোনো Node এর External/Internal IP নিন
func (k *K8sClient) getNodeIP() (string, error) {
	nodes, err := k.client.CoreV1().Nodes().List(context.Background(), metav1.ListOptions{})
	if err != nil {
		return "", err
	}
	if len(nodes.Items) == 0 {
		return "", fmt.Errorf("কোনো node নেই")
	}

	// প্রথম node এর IP নিন (ExternalIP আগে, না থাকলে InternalIP)
	for _, addr := range nodes.Items[0].Status.Addresses {
		if addr.Type == corev1.NodeExternalIP {
			return addr.Address, nil
		}
	}
	for _, addr := range nodes.Items[0].Status.Addresses {
		if addr.Type == corev1.NodeInternalIP {
			return addr.Address, nil
		}
	}

	return "", fmt.Errorf("node IP পাওয়া যায়নি")
}

func (k *K8sClient) StartStream(cfg StreamConfig) (*StreamInfo, error) {
	if cfg.Namespace == "" {
		cfg.Namespace = "default"
	}

	// আগে আছে কিনা চেক
	_, err := k.client.AppsV1().Deployments(cfg.Namespace).Get(
		context.Background(), cfg.Name, metav1.GetOptions{},
	)
	if err == nil {
		return nil, fmt.Errorf("stream '%s' already running", cfg.Name)
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
					// HostNetwork সরানো হয়েছে ✅
					// প্রতিটা Pod আলাদা IP পাবে, port conflict নেই
					TerminationGracePeriodSeconds: int64Ptr(0),
					Containers: []corev1.Container{
						{
							Name:            cfg.Name,
							Image:           "streamenginegopro:latest",
							ImagePullPolicy: corev1.PullNever,
							Ports: []corev1.ContainerPort{
								{ContainerPort: 8080}, // সব Pod এ same — কোনো সমস্যা নেই
							},
							Env: []corev1.EnvVar{
								{Name: "STREAM_ID", Value: cfg.StreamID},
								{Name: "VIDEO_URLS", Value: cfg.VideoURLs},
								{Name: "DEFAULT_OUTPUT_URL", Value: cfg.OutputURL},
								{Name: "DEFAULT_OUTPUT_ID", Value: cfg.OutputID},
								{
									Name:  "API_KEY",
									Value: os.Getenv("API_KEY"),
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
		return nil, fmt.Errorf("deployment create error: %w", err)
	}

	// ── NodePort Service create ──
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cfg.Name + "-svc", // stream-name-svc
			Namespace: cfg.Namespace,
			Labels: map[string]string{
				"app":  cfg.Name,
				"type": "stream-server",
			},
		},
		Spec: corev1.ServiceSpec{
			Type: corev1.ServiceTypeNodePort,
			Selector: map[string]string{
				"app": cfg.Name, // deployment label এর সাথে match
			},
			Ports: []corev1.ServicePort{
				{
					Port:       8080,
					TargetPort: k8s_intstr.FromInt(8080),
					// NodePort দেওয়া হয়নি → K8s 30000-32767 থেকে auto assign করবে ✅
				},
			},
		},
	}

	createdSvc, err := k.client.CoreV1().Services(cfg.Namespace).Create(
		context.Background(), svc, metav1.CreateOptions{},
	)
	if err != nil {
		// Service create fail হলে Deployment ও মুছে দিন
		_ = k.client.AppsV1().Deployments(cfg.Namespace).Delete(
			context.Background(), cfg.Name, metav1.DeleteOptions{},
		)
		return nil, fmt.Errorf("service create error: %w", err)
	}

	assignedPort := createdSvc.Spec.Ports[0].NodePort

	info := &StreamInfo{
		Name:      cfg.Name,
		Namespace: cfg.Namespace,
		Port:      assignedPort,
		AccessURL: fmt.Sprintf("http://%s:%d", k.nodeIP, assignedPort),
	}

	fmt.Printf("✅ Stream '%s' started → %s\n", cfg.Name, info.AccessURL)
	return info, nil
}

func (k *K8sClient) StopStream(name, namespace string) error {
	if namespace == "" {
		namespace = "default"
	}

	// Deployment delete
	err := k.client.AppsV1().Deployments(namespace).Delete(
		context.Background(), name, metav1.DeleteOptions{},
	)
	if errors.IsNotFound(err) {
		return fmt.Errorf("stream '%s' not found", name)
	}
	if err != nil {
		return fmt.Errorf("deployment delete error: %w", err)
	}

	// Service ও delete ✅
	err = k.client.CoreV1().Services(namespace).Delete(
		context.Background(), name+"-svc", metav1.DeleteOptions{},
	)
	if err != nil && !errors.IsNotFound(err) {
		return fmt.Errorf("service delete error: %w", err)
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

		// Service থেকে assigned port নিন
		port := "unknown"
		svc, err := k.client.CoreV1().Services(namespace).Get(
			context.Background(), d.Name+"-svc", metav1.GetOptions{},
		)
		if err == nil && len(svc.Spec.Ports) > 0 {
			port = fmt.Sprintf("%d", svc.Spec.Ports[0].NodePort)
		}

		streams = append(streams, map[string]string{
			"name":       d.Name,
			"ready":      fmt.Sprintf("%d/%d", d.Status.ReadyReplicas, d.Status.Replicas),
			"status":     status,
			"port":       port,
			"access_url": fmt.Sprintf("http://%s:%s", k.nodeIP, port),
		})
	}

	return streams, nil
}

func int64Ptr(i int64) *int64 { return &i }
