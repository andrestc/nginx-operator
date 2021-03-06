package k8s

import (
	"encoding/json"
	"fmt"

	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/tsuru/nginx-operator/pkg/apis/nginx/v1alpha1"

	appv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const (
	// Default docker image used for nginx
	defaultNginxImage = "nginx:latest"

	// Default port names used by the nginx container and the ClusterIP service
	defaultHTTPPortName  = "http"
	defaultHTTPSPortName = "https"

	// Mount path where nginx.conf will be placed
	configMountPath = "/etc/nginx"

	// Mount path where certificate and key pair will be placed
	certMountPath = configMountPath + "/certs"

	// Annotation key used to stored the nginx that created the deployment
	generatedFromAnnotation = "nginx.tsuru.io/generated-from"
)

// NewDeployment creates a deployment for a given Nginx resource.
func NewDeployment(n *v1alpha1.Nginx) (*appv1.Deployment, error) {
	n.Spec.Image = valueOrDefault(n.Spec.Image, defaultNginxImage)
	deployment := appv1.Deployment{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Deployment",
			APIVersion: "apps/v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      n.Name + "-deployment",
			Namespace: n.Namespace,
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(n, schema.GroupVersionKind{
					Group:   v1alpha1.SchemeGroupVersion.Group,
					Version: v1alpha1.SchemeGroupVersion.Version,
					Kind:    "Nginx",
				}),
			},
		},
		Spec: appv1.DeploymentSpec{
			Replicas: n.Spec.Replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: LabelsForNginx(n.Name),
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: n.Namespace,
					Labels:    LabelsForNginx(n.Name),
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "nginx",
							Image: n.Spec.Image,
							Ports: []corev1.ContainerPort{
								{
									Name:          defaultHTTPPortName,
									ContainerPort: int32(80),
									Protocol:      corev1.ProtocolTCP,
								},
							},
							Resources: n.Spec.PodTemplate.Resources,
							ReadinessProbe: &corev1.Probe{
								Handler: corev1.Handler{
									HTTPGet: &corev1.HTTPGetAction{
										Path:   "/",
										Port:   intstr.FromString(defaultHTTPPortName),
										Scheme: corev1.URISchemeHTTP,
									},
								},
							},
						},
					},
					Affinity: n.Spec.PodTemplate.Affinity,
				},
			},
		},
	}
	setupConfig(n.Spec.Config, &deployment)
	setupTLS(n.Spec.TLSSecret, &deployment)

	// This is done on the last step because n.Spec may have mutated during these methods
	if err := SetNginxSpec(&deployment.ObjectMeta, n.Spec); err != nil {
		return nil, err
	}

	return &deployment, nil
}

// NewService assembles the ClusterIP service for the Nginx
func NewService(n *v1alpha1.Nginx) *corev1.Service {
	service := corev1.Service{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Service",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      n.Name + "-service",
			Namespace: n.Namespace,
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(n, schema.GroupVersionKind{
					Group:   v1alpha1.SchemeGroupVersion.Group,
					Version: v1alpha1.SchemeGroupVersion.Version,
					Kind:    "Nginx",
				}),
			},
			Labels: LabelsForNginx(n.Name),
		},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{
				{
					Name:       defaultHTTPPortName,
					Protocol:   corev1.ProtocolTCP,
					TargetPort: intstr.FromString(defaultHTTPPortName),
					Port:       int32(80),
				},
			},
			Selector: LabelsForNginx(n.Name),
			Type:     corev1.ServiceTypeClusterIP,
		},
	}
	if n.Spec.TLSSecret != nil {
		service.Spec.Ports = append(service.Spec.Ports, corev1.ServicePort{
			Name:       defaultHTTPSPortName,
			Protocol:   corev1.ProtocolTCP,
			TargetPort: intstr.FromString(defaultHTTPSPortName),
			Port:       int32(443),
		})
	}
	return &service
}

// LabelsForNginx returns the labels for a Nginx CR with the given name
func LabelsForNginx(name string) map[string]string {
	return map[string]string{
		"nginx_cr": name,
		"app":      "nginx",
	}
}

// ExtractNginxSpec extracts the nginx used to create the object
func ExtractNginxSpec(o metav1.ObjectMeta) (v1alpha1.NginxSpec, error) {
	ann, ok := o.Annotations[generatedFromAnnotation]
	if !ok {
		return v1alpha1.NginxSpec{}, fmt.Errorf("missing %q annotation in deployment", generatedFromAnnotation)
	}
	var spec v1alpha1.NginxSpec
	if err := json.Unmarshal([]byte(ann), &spec); err != nil {
		return v1alpha1.NginxSpec{}, fmt.Errorf("failed to unmarshal nginx from annotation: %v", err)
	}
	return spec, nil
}

// SetNginxSpec sets the nginx spec into the object annotation to be later extracted
func SetNginxSpec(o *metav1.ObjectMeta, spec v1alpha1.NginxSpec) error {
	if o.Annotations == nil {
		o.Annotations = make(map[string]string)
	}
	origSpec, err := json.Marshal(spec)
	if err != nil {
		return err
	}
	o.Annotations[generatedFromAnnotation] = string(origSpec)
	return nil
}

func setupConfig(conf *v1alpha1.ConfigRef, dep *appv1.Deployment) {
	if conf == nil {
		return
	}
	dep.Spec.Template.Spec.Containers[0].VolumeMounts = append(dep.Spec.Template.Spec.Containers[0].VolumeMounts, corev1.VolumeMount{
		Name:      "nginx-config",
		MountPath: configMountPath,
	})
	switch conf.Kind {
	case v1alpha1.ConfigKindConfigMap:
		dep.Spec.Template.Spec.Volumes = append(dep.Spec.Template.Spec.Volumes, corev1.Volume{
			Name: "nginx-config",
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: conf.Name,
					},
				},
			},
		})
	case v1alpha1.ConfigKindInline:
		if dep.Spec.Template.Annotations == nil {
			dep.Spec.Template.Annotations = make(map[string]string)
		}
		dep.Spec.Template.Annotations[conf.Name] = conf.Value
		dep.Spec.Template.Spec.Volumes = append(dep.Spec.Template.Spec.Volumes, corev1.Volume{
			Name: "nginx-config",
			VolumeSource: corev1.VolumeSource{
				DownwardAPI: &corev1.DownwardAPIVolumeSource{
					Items: []corev1.DownwardAPIVolumeFile{
						{
							Path: "nginx.conf",
							FieldRef: &corev1.ObjectFieldSelector{
								FieldPath: fmt.Sprintf("metadata.annotations['%s']", conf.Name),
							},
						},
					},
				},
			},
		})
	}
}

// setupTLS appends an https port if TLS secrets are specified
func setupTLS(secret *v1alpha1.TLSSecret, dep *appv1.Deployment) {
	if secret == nil {
		return
	}

	dep.Spec.Template.Spec.Containers[0].Ports = append(dep.Spec.Template.Spec.Containers[0].Ports, corev1.ContainerPort{
		Name:          defaultHTTPSPortName,
		ContainerPort: int32(443),
		Protocol:      corev1.ProtocolTCP,
	})
	dep.Spec.Template.Spec.Containers[0].ReadinessProbe = &corev1.Probe{
		Handler: corev1.Handler{
			HTTPGet: &corev1.HTTPGetAction{
				Path:   "/",
				Port:   intstr.FromString(defaultHTTPSPortName),
				Scheme: corev1.URISchemeHTTPS,
			},
		},
	}
	dep.Spec.Template.Spec.Containers[0].VolumeMounts = append(dep.Spec.Template.Spec.Containers[0].VolumeMounts, corev1.VolumeMount{
		Name:      "nginx-certs",
		MountPath: certMountPath,
	})

	secret.KeyField = valueOrDefault(secret.KeyField, "tls.key")
	secret.CertificateField = valueOrDefault(secret.CertificateField, "tls.crt")
	secret.KeyPath = valueOrDefault(secret.KeyPath, secret.KeyField)
	secret.CertificatePath = valueOrDefault(secret.CertificatePath, secret.CertificateField)

	dep.Spec.Template.Spec.Volumes = append(dep.Spec.Template.Spec.Volumes, corev1.Volume{
		Name: "nginx-certs",
		VolumeSource: corev1.VolumeSource{
			Secret: &corev1.SecretVolumeSource{
				SecretName: secret.SecretName,
				Items: []corev1.KeyToPath{
					{Key: secret.KeyField, Path: secret.KeyPath},
					{Key: secret.CertificateField, Path: secret.CertificatePath},
				},
			},
		},
	})
}

func valueOrDefault(value, def string) string {
	if value != "" {
		return value
	}
	return def
}
