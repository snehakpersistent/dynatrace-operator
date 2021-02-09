package capability

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"strconv"

	"github.com/Dynatrace/dynatrace-operator/api/v1alpha1"
	"github.com/Dynatrace/dynatrace-operator/controllers/customproperties"
	"github.com/Dynatrace/dynatrace-operator/controllers/dtpullsecret"
	"github.com/Dynatrace/dynatrace-operator/controllers/utils"
	"github.com/pkg/errors"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
)

const (
	serviceAccountPrefix = "dynatrace-"

	kubernetesArch     = "kubernetes.io/arch"
	kubernetesOS       = "kubernetes.io/os"
	kubernetesBetaArch = "beta.kubernetes.io/arch"
	kubernetesBetaOS   = "beta.kubernetes.io/os"

	amd64 = "amd64"
	arm64 = "arm64"
	linux = "linux"

	annotationTemplateHash    = "internal.operator.dynatrace.com/template-hash"
	annotationImageHash       = "internal.operator.dynatrace.com/image-hash"
	annotationImageVersion    = "internal.operator.dynatrace.com/image-version"
	annotationCustomPropsHash = "internal.operator.dynatrace.com/custom-properties-hash"

	DTCapabilities    = "DT_CAPABILITIES"
	DTIdSeedNamespace = "DT_ID_SEED_NAMESPACE"
	DTIdSeedClusterId = "DT_ID_SEED_K8S_CLUSTER_ID"

	DTCapabilitiesArg = "--enable=$(DT_CAPABILITIES)"

	ProxyArg = `PROXY="${ACTIVE_GATE_PROXY}"`
	ProxyEnv = "ACTIVE_GATE_PROXY"
	ProxyKey = "ProxyKey"

	keyModule = "module"
)

type StatefulSetEvent func(sts *appsv1.StatefulSet)

type statefulSetProperties struct {
	*v1alpha1.DynaKube
	*v1alpha1.CapabilityProperties
	customPropertiesHash string
	kubeSystemUID        types.UID
	module               string
	capabilityName       string
	serviceAccountOwner  string
	onAfterCreate        []StatefulSetEvent
}

func NewStatefulSetProperties(instance *v1alpha1.DynaKube, capabilityProperties *v1alpha1.CapabilityProperties,
	kubeSystemUID types.UID, customPropertiesHash string, module string, capabilityName string, serviceAccountOwner string) *statefulSetProperties {
	if serviceAccountOwner == "" {
		serviceAccountOwner = module
	}

	return &statefulSetProperties{
		DynaKube:             instance,
		CapabilityProperties: capabilityProperties,
		customPropertiesHash: customPropertiesHash,
		kubeSystemUID:        kubeSystemUID,
		module:               module,
		capabilityName:       capabilityName,
		serviceAccountOwner:  serviceAccountOwner,
		onAfterCreate:        []StatefulSetEvent{},
	}
}

func CreateStatefulSet(stsProperties *statefulSetProperties) (*appsv1.StatefulSet, error) {
	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      stsProperties.Name + "-" + stsProperties.module,
			Namespace: stsProperties.Namespace,
			Labels: MergeLabels(
				BuildLabels(stsProperties.DynaKube, stsProperties.CapabilityProperties),
				map[string]string{keyModule: stsProperties.module}),
			Annotations: map[string]string{},
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas:            stsProperties.Replicas,
			PodManagementPolicy: appsv1.ParallelPodManagement,
			Selector:            &metav1.LabelSelector{MatchLabels: BuildLabelsFromInstance(stsProperties.DynaKube)},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: BuildLabels(stsProperties.DynaKube, stsProperties.CapabilityProperties),
					Annotations: map[string]string{
						annotationImageHash:       stsProperties.Status.ActiveGate.ImageHash,
						annotationImageVersion:    stsProperties.Status.ActiveGate.ImageVersion,
						annotationCustomPropsHash: stsProperties.customPropertiesHash,
					},
				},
				Spec: buildTemplateSpec(stsProperties),
			},
		}}

	for _, onAfterCreate := range stsProperties.onAfterCreate {
		onAfterCreate(sts)
	}

	hash, err := generateStatefulSetHash(sts)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	sts.ObjectMeta.Annotations[annotationTemplateHash] = hash
	return sts, nil
}

func buildTemplateSpec(stsProperties *statefulSetProperties) corev1.PodSpec {
	return corev1.PodSpec{
		Containers:         []corev1.Container{buildContainer(stsProperties)},
		NodeSelector:       stsProperties.NodeSelector,
		ServiceAccountName: determineServiceAccountName(stsProperties),
		Affinity: &corev1.Affinity{
			NodeAffinity: &corev1.NodeAffinity{
				RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
					NodeSelectorTerms: []corev1.NodeSelectorTerm{
						{MatchExpressions: buildKubernetesExpression(kubernetesBetaArch, kubernetesBetaOS)},
						{MatchExpressions: buildKubernetesExpression(kubernetesArch, kubernetesOS)},
					}}}},
		Tolerations: stsProperties.Tolerations,
		Volumes:     buildVolumes(stsProperties),
		ImagePullSecrets: []corev1.LocalObjectReference{
			{Name: stsProperties.Name + dtpullsecret.PullSecretSuffix},
		},
	}
}

func buildContainer(stsProperties *statefulSetProperties) corev1.Container {
	return corev1.Container{
		Name:            v1alpha1.OperatorName,
		Image:           utils.BuildActiveGateImage(stsProperties.DynaKube),
		Resources:       BuildResources(stsProperties.DynaKube),
		ImagePullPolicy: corev1.PullAlways,
		Env:             buildEnvs(stsProperties),
		Args:            buildArgs(stsProperties),
		VolumeMounts:    buildVolumeMounts(stsProperties),
		ReadinessProbe: &corev1.Probe{
			Handler: corev1.Handler{
				HTTPGet: &corev1.HTTPGetAction{
					Path:   "/rest/health",
					Port:   intstr.IntOrString{IntVal: 9999},
					Scheme: "HTTPS",
				},
			},
			InitialDelaySeconds: 90,
			PeriodSeconds:       15,
			FailureThreshold:    3,
		},
		LivenessProbe: &corev1.Probe{
			Handler: corev1.Handler{
				HTTPGet: &corev1.HTTPGetAction{
					Path:   "/rest/state",
					Port:   intstr.IntOrString{IntVal: 9999},
					Scheme: "HTTPS",
				},
			},
			InitialDelaySeconds: 90,
			PeriodSeconds:       30,
			FailureThreshold:    2,
		},
	}
}

func buildVolumes(stsProperties *statefulSetProperties) []corev1.Volume {
	var volumes []corev1.Volume

	if !isCustomPropertiesNilOrEmpty(stsProperties.CustomProperties) {
		valueFrom := determineCustomPropertiesSource(stsProperties)
		volumes = append(volumes, corev1.Volume{
			Name: customproperties.VolumeName,
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName: valueFrom,
					Items: []corev1.KeyToPath{
						{Key: customproperties.DataKey, Path: customproperties.DataPath},
					}}}},
		)
	}

	return volumes
}

func determineCustomPropertiesSource(stsProperties *statefulSetProperties) string {
	if stsProperties.CustomProperties.ValueFrom == "" {
		return fmt.Sprintf("%s-%s-%s", stsProperties.Name, stsProperties.serviceAccountOwner, customproperties.Suffix)
	}
	return stsProperties.CustomProperties.ValueFrom
}

func buildVolumeMounts(stsProperties *statefulSetProperties) []corev1.VolumeMount {
	var volumeMounts []corev1.VolumeMount

	if !isCustomPropertiesNilOrEmpty(stsProperties.CustomProperties) {
		volumeMounts = append(volumeMounts, corev1.VolumeMount{
			ReadOnly:  true,
			Name:      customproperties.VolumeName,
			MountPath: customproperties.MountPath,
			SubPath:   customproperties.DataPath,
		})
	}

	return volumeMounts
}

func buildEnvs(stsProperties *statefulSetProperties) []corev1.EnvVar {
	envs := []corev1.EnvVar{
		{Name: DTCapabilities, Value: stsProperties.capabilityName},
		{Name: DTIdSeedNamespace, Value: stsProperties.Namespace},
		{Name: DTIdSeedClusterId, Value: string(stsProperties.kubeSystemUID)},
	}
	envs = append(envs, stsProperties.Env...)

	if !isProxyNilOrEmpty(stsProperties.Spec.Proxy) {
		envs = append(envs, buildProxyEnv(stsProperties.Spec.Proxy))
	}

	return envs
}

func buildProxyEnv(proxy *v1alpha1.DynaKubeProxy) corev1.EnvVar {
	if proxy.ValueFrom != "" {
		return corev1.EnvVar{
			Name: ProxyEnv,
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: proxy.ValueFrom},
					Key:                  ProxyKey,
				},
			},
		}
	} else {
		return corev1.EnvVar{
			Name:  ProxyEnv,
			Value: proxy.Value,
		}
	}
}

func buildArgs(stsProperties *statefulSetProperties) []string {
	group := stsProperties.Group
	args := []string{
		DTCapabilitiesArg,
	}
	args = append(args, stsProperties.Args...)

	if stsProperties.Spec.NetworkZone != "" {
		args = append(args, fmt.Sprintf(`--networkzone="%s"`, stsProperties.Spec.NetworkZone))
	}
	if !isProxyNilOrEmpty(stsProperties.Spec.Proxy) {
		args = append(args, ProxyArg)
	}
	if group != "" {
		args = append(args, fmt.Sprintf(`--group="%s"`, group))
	}

	return args
}

func determineServiceAccountName(stsProperties *statefulSetProperties) string {
	if stsProperties.ServiceAccountName == "" {
		return serviceAccountPrefix + stsProperties.serviceAccountOwner
	}
	return stsProperties.ServiceAccountName
}

func buildKubernetesExpression(archKey string, osKey string) []corev1.NodeSelectorRequirement {
	return []corev1.NodeSelectorRequirement{
		{
			Key:      archKey,
			Operator: corev1.NodeSelectorOpIn,
			Values:   []string{amd64, arm64},
		},
		{
			Key:      osKey,
			Operator: corev1.NodeSelectorOpIn,
			Values:   []string{linux},
		},
	}
}

func isCustomPropertiesNilOrEmpty(customProperties *v1alpha1.DynaKubeValueSource) bool {
	return customProperties == nil ||
		(customProperties.Value == "" &&
			customProperties.ValueFrom == "")
}

func isProxyNilOrEmpty(proxy *v1alpha1.DynaKubeProxy) bool {
	return proxy == nil || (proxy.Value == "" && proxy.ValueFrom == "")
}

func generateStatefulSetHash(sts *appsv1.StatefulSet) (string, error) {
	data, err := json.Marshal(sts)
	if err != nil {
		return "", errors.WithStack(err)
	}

	hasher := fnv.New32()
	_, err = hasher.Write(data)
	if err != nil {
		return "", errors.WithStack(err)
	}

	return strconv.FormatUint(uint64(hasher.Sum32()), 10), nil
}

func HasStatefulSetChanged(a *appsv1.StatefulSet, b *appsv1.StatefulSet) bool {
	return GetTemplateHash(a) != GetTemplateHash(b)
}

func GetTemplateHash(a metav1.Object) string {
	if annotations := a.GetAnnotations(); annotations != nil {
		return annotations[annotationTemplateHash]
	}
	return ""
}
