package kubemon

import (
	"context"
	"fmt"
	"testing"

	"github.com/Dynatrace/dynatrace-operator/api/v1alpha1"
	"github.com/Dynatrace/dynatrace-operator/controllers/customproperties"
	"github.com/Dynatrace/dynatrace-operator/controllers/dtversion"
	"github.com/Dynatrace/dynatrace-operator/controllers/kubesystem"
	"github.com/Dynatrace/dynatrace-operator/dtclient"
	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	testPaasToken = "test-paas-token"
)

func init() {
	utilruntime.Must(scheme.AddToScheme(scheme.Scheme))
	utilruntime.Must(v1alpha1.AddToScheme(scheme.Scheme))
}

var mockImageVersionProvider dtversion.ImageVersionProvider = func(image string, _ *dtversion.DockerConfig) (dtversion.ImageVersion, error) {
	return dtversion.ImageVersion{
		Version: "1.0.0.0",
		Hash:    "",
	}, nil
}

func TestReconciler_Reconcile(t *testing.T) {
	t.Run(`Reconcile reconciles minimal setup`, func(t *testing.T) {
		log := logf.Log.WithName("TestReconciler")
		dtcMock := &dtclient.MockDynatraceClient{}
		instance := &v1alpha1.DynaKube{
			ObjectMeta: metav1.ObjectMeta{
				Name: testName,
			}}
		secret := buildTestPaasTokenSecret()
		fakeClient := fake.NewClientBuilder().WithScheme(scheme.Scheme).WithObjects(
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
				UID:  testUID,
				Name: kubesystem.Namespace,
			}},
			instance, secret).Build()
		reconciler := NewReconciler(
			fakeClient, fakeClient, scheme.Scheme, dtcMock, log, instance, mockImageVersionProvider,
		)
		connectionInfo := dtclient.ConnectionInfo{TenantUUID: testUID}
		tenantInfo := &dtclient.TenantInfo{ID: testUID}

		dtcMock.
			On("GetConnectionInfo").
			Return(connectionInfo, nil)

		dtcMock.
			On("GetTenantInfo").
			Return(tenantInfo, nil)

		assert.NotNil(t, reconciler)

		result, err := reconciler.Reconcile()

		assert.NoError(t, err)
		assert.NotNil(t, result)

		var statefulSet v1.StatefulSet
		err = fakeClient.Get(context.TODO(), client.ObjectKey{Name: instance.Name + StatefulSetSuffix, Namespace: instance.Namespace}, &statefulSet)
		assert.NoError(t, err)

		expected, err := newStatefulSet(instance, testUID, "")
		assert.NoError(t, err)

		expected.Spec.Template.Spec.Volumes = nil

		assert.NoError(t, err)
		assert.NotNil(t, statefulSet)
		assert.Equal(t, expected.ObjectMeta.Name, statefulSet.ObjectMeta.Name)
		assert.Equal(t, expected.ObjectMeta.Namespace, statefulSet.ObjectMeta.Namespace)
		assert.Equal(t, expected.Spec, statefulSet.Spec)
	})
	t.Run(`Reconcile reconciles custom properties if set`, func(t *testing.T) {
		log := logf.Log.WithName("TestReconciler")
		dtcMock := &dtclient.MockDynatraceClient{}
		instance := &v1alpha1.DynaKube{
			ObjectMeta: metav1.ObjectMeta{
				Name:      testName,
				Namespace: testNamespace,
			},
			Spec: v1alpha1.DynaKubeSpec{
				KubernetesMonitoringSpec: v1alpha1.KubernetesMonitoringSpec{
					CapabilityProperties: v1alpha1.CapabilityProperties{
						CustomProperties: &v1alpha1.DynaKubeValueSource{
							Value: testValue,
						},
					},
				}}}
		secret := buildTestPaasTokenSecret()
		fakeClient := fake.NewClientBuilder().WithScheme(scheme.Scheme).WithObjects(
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
				UID:  testUID,
				Name: kubesystem.Namespace,
			}},
			instance, secret).Build()
		reconciler := NewReconciler(
			fakeClient, fakeClient, scheme.Scheme, dtcMock, log, instance, mockImageVersionProvider,
		)
		connectionInfo := dtclient.ConnectionInfo{TenantUUID: testUID}
		tenantInfo := &dtclient.TenantInfo{ID: testUID}

		dtcMock.
			On("GetConnectionInfo").
			Return(connectionInfo, nil)

		dtcMock.
			On("GetTenantInfo").
			Return(tenantInfo, nil)

		assert.NotNil(t, reconciler)

		result, err := reconciler.Reconcile()

		assert.NoError(t, err)
		assert.NotNil(t, result)

		var customPropertiesSecret corev1.Secret
		err = fakeClient.Get(context.TODO(), client.ObjectKey{Name: fmt.Sprintf("%s-%s-%s", instance.Name, Name, customproperties.Suffix), Namespace: testNamespace}, &customPropertiesSecret)

		assert.NoError(t, err)
		assert.NotNil(t, customPropertiesSecret)
		assert.Equal(t, testValue, string(customPropertiesSecret.Data[customproperties.DataKey]))
	})
}

func buildTestPaasTokenSecret() *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testName,
			Namespace: testNamespace,
		},
		Data: map[string][]byte{dtclient.DynatracePaasToken: []byte(testPaasToken)},
	}
}
