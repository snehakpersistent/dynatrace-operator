package routing

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Dynatrace/dynatrace-operator/api/v1alpha1"
	"github.com/Dynatrace/dynatrace-operator/controllers/capability"
	"github.com/Dynatrace/dynatrace-operator/controllers/customproperties"
	"github.com/Dynatrace/dynatrace-operator/controllers/dtpullsecret"
	"github.com/Dynatrace/dynatrace-operator/controllers/dtversion"
	"github.com/Dynatrace/dynatrace-operator/controllers/kubesystem"
	"github.com/Dynatrace/dynatrace-operator/dtclient"
	"github.com/Dynatrace/dynatrace-operator/logger"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/deprecated/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const (
	testValue     = "test-value"
	testUID       = "test-uid"
	testName      = "test-name"
	testNamespace = "test-namespace"
	testKey       = "test-key"
	testVersion   = "1.0.0"
)

func init() {
	utilruntime.Must(scheme.AddToScheme(scheme.Scheme))
	utilruntime.Must(v1alpha1.AddToScheme(scheme.Scheme))
}

func TestNewReconiler(t *testing.T) {
	createDefaultReconciler(t)
}

func createDefaultReconciler(t *testing.T) *ReconcileRouting {
	log := logger.NewDTLogger()
	clt := fake.NewClientBuilder().
		WithScheme(scheme.Scheme).
		WithObjects(&corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: kubesystem.Namespace,
				UID:  testUID,
			},
		}).
		Build()
	dtc := &dtclient.MockDynatraceClient{}
	instance := &v1alpha1.DynaKube{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: testNamespace,
		}}
	imgVerProvider := func(img string, dockerConfig *dtversion.DockerConfig) (dtversion.ImageVersion, error) {
		return dtversion.ImageVersion{}, nil
	}

	r := NewReconciler(clt, clt, scheme.Scheme, dtc, log, instance, imgVerProvider, false)
	require.NotNil(t, r)
	require.NotNil(t, r.Client)
	require.NotNil(t, r.scheme)
	require.NotNil(t, r.dtc)
	require.NotNil(t, r.log)
	require.NotNil(t, r.instance)
	require.NotNil(t, r.imageVersionProvider)

	return r
}

func TestReconcile(t *testing.T) {
	t.Run(`reconcile custom properties`, func(t *testing.T) {
		r := createDefaultReconciler(t)
		r.instance.Spec.RoutingSpec.CustomProperties = &v1alpha1.DynaKubeValueSource{
			Value: testValue,
		}
		_, err := r.Reconcile()

		assert.NoError(t, err)

		var customProperties corev1.Secret
		err = r.Get(context.TODO(), client.ObjectKey{Name: r.instance.Name + "-" + module + "-" + customproperties.Suffix, Namespace: r.instance.Namespace}, &customProperties)
		assert.NoError(t, err)
		assert.NotNil(t, customProperties)
		assert.Contains(t, customProperties.Data, customproperties.DataKey)
		assert.Equal(t, testValue, string(customProperties.Data[customproperties.DataKey]))
	})
	t.Run(`create stateful set`, func(t *testing.T) {
		r := createDefaultReconciler(t)
		update, err := r.Reconcile()

		assert.True(t, update)
		assert.NoError(t, err)

		statefulSet := &v1.StatefulSet{}
		err = r.Get(context.TODO(), client.ObjectKey{Name: r.instance.Name + StatefulSetSuffix, Namespace: r.instance.Namespace}, statefulSet)

		assert.NotNil(t, statefulSet)
		assert.NoError(t, err)
	})
	t.Run(`update stateful set`, func(t *testing.T) {
		r := createDefaultReconciler(t)
		update, err := r.Reconcile()

		assert.True(t, update)
		assert.NoError(t, err)

		statefulSet := &v1.StatefulSet{}
		err = r.Get(context.TODO(), client.ObjectKey{Name: r.instance.Name + StatefulSetSuffix, Namespace: r.instance.Namespace}, statefulSet)

		assert.NotNil(t, statefulSet)
		assert.NoError(t, err)

		r.instance.Spec.Proxy = &v1alpha1.DynaKubeProxy{Value: testValue}
		update, err = r.Reconcile()

		assert.True(t, update)
		assert.NoError(t, err)

		newStatefulSet := &v1.StatefulSet{}
		err = r.Get(context.TODO(), client.ObjectKey{Name: r.instance.Name + StatefulSetSuffix, Namespace: r.instance.Namespace}, newStatefulSet)

		assert.NotNil(t, statefulSet)
		assert.NoError(t, err)

		found := false
		for _, env := range newStatefulSet.Spec.Template.Spec.Containers[0].Env {
			if env.Name == capability.ProxyEnv {
				found = true
				assert.Equal(t, testValue, env.Value)
			}
		}
		assert.True(t, found)
	})
}

func TestReconcile_GetStatefulSet(t *testing.T) {
	r := createDefaultReconciler(t)
	update, err := r.Reconcile()
	assert.True(t, update)
	assert.NoError(t, err)

	desiredSts, err := r.buildDesiredStatefulSet()
	assert.NoError(t, err)
	assert.NotNil(t, desiredSts)

	desiredSts.Kind = "StatefulSet"
	desiredSts.APIVersion = "apps/v1"
	desiredSts.ResourceVersion = "1"
	err = controllerutil.SetControllerReference(r.instance, desiredSts, r.scheme)
	require.NoError(t, err)

	sts, err := r.getStatefulSet(desiredSts)
	assert.NoError(t, err)
	assert.Equal(t, *desiredSts, *sts)
}

func TestReconcile_CreateStatefulSetIfNotExists(t *testing.T) {
	r := createDefaultReconciler(t)
	desiredSts, err := r.buildDesiredStatefulSet()
	require.NoError(t, err)
	require.NotNil(t, desiredSts)

	created, err := r.createStatefulSetIfNotExists(desiredSts)
	assert.NoError(t, err)
	assert.True(t, created)

	created, err = r.createStatefulSetIfNotExists(desiredSts)
	assert.NoError(t, err)
	assert.False(t, created)
}

func TestReconcile_UpdateStatefulSetIfOutdated(t *testing.T) {
	r := createDefaultReconciler(t)
	desiredSts, err := r.buildDesiredStatefulSet()
	require.NoError(t, err)
	require.NotNil(t, desiredSts)

	updated, err := r.updateStatefulSetIfOutdated(desiredSts)
	assert.Error(t, err)
	assert.False(t, updated)
	assert.True(t, k8serrors.IsNotFound(errors.Cause(err)))

	created, err := r.createStatefulSetIfNotExists(desiredSts)
	require.True(t, created)
	require.NoError(t, err)

	updated, err = r.updateStatefulSetIfOutdated(desiredSts)
	assert.NoError(t, err)
	assert.False(t, updated)

	r.instance.Spec.Proxy = &v1alpha1.DynaKubeProxy{Value: testValue}
	desiredSts, err = r.buildDesiredStatefulSet()
	require.NoError(t, err)

	updated, err = r.updateStatefulSetIfOutdated(desiredSts)
	assert.NoError(t, err)
	assert.True(t, updated)
}

func TestReconcile_GetCustomPropertyHash(t *testing.T) {
	r := createDefaultReconciler(t)
	hash, err := r.calculateCustomPropertyHash()
	assert.NoError(t, err)
	assert.Empty(t, hash)

	r.instance.Spec.RoutingSpec.CustomProperties = &v1alpha1.DynaKubeValueSource{Value: testValue}
	hash, err = r.calculateCustomPropertyHash()
	assert.NoError(t, err)
	assert.NotEmpty(t, hash)

	r.instance.Spec.RoutingSpec.CustomProperties = &v1alpha1.DynaKubeValueSource{ValueFrom: testName}
	hash, err = r.calculateCustomPropertyHash()
	assert.Error(t, err)
	assert.Empty(t, hash)

	err = r.Create(context.TODO(), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testName,
			Namespace: testNamespace,
		},
		Data: map[string][]byte{
			customproperties.DataKey: []byte(testValue),
		},
	})
	hash, err = r.calculateCustomPropertyHash()
	assert.NoError(t, err)
	assert.NotEmpty(t, hash)
}

func TestReconcile_UpdateImageVersion(t *testing.T) {
	r := createDefaultReconciler(t)
	updated, err := r.updateImageVersion()
	assert.NoError(t, err)
	assert.False(t, updated)

	r.enableUpdates = true
	updated, err = r.updateImageVersion()
	assert.Error(t, err)
	assert.False(t, updated)

	data, err := buildTestDockerAuth(t)
	require.NoError(t, err)

	err = createTestPullSecret(t, r, data)
	require.NoError(t, err)

	r.imageVersionProvider = func(img string, dockerConfig *dtversion.DockerConfig) (dtversion.ImageVersion, error) {
		return dtversion.ImageVersion{
			Version: testVersion,
			Hash:    testValue,
		}, nil
	}
	updated, err = r.updateImageVersion()
	assert.NoError(t, err)
	assert.True(t, updated)

	r.instance.Status.ActiveGate.ImageVersion = testVersion
	r.instance.Status.ActiveGate.ImageHash = testValue

	updated, err = r.updateImageVersion()
	assert.NoError(t, err)
	assert.False(t, updated)
}

// Adding *testing.T parameter to prevent usage in production code
func createTestPullSecret(_ *testing.T, r *ReconcileRouting, data []byte) error {
	return r.Create(context.TODO(), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: r.instance.Namespace,
			Name:      r.instance.Name + dtpullsecret.PullSecretSuffix,
		},
		Data: map[string][]byte{
			".dockerconfigjson": data,
		},
	})
}

// Adding *testing.T parameter to prevent usage in production code
func buildTestDockerAuth(_ *testing.T) ([]byte, error) {
	dockerConf := struct {
		Auths map[string]dtversion.DockerAuth `json:"auths"`
	}{
		Auths: map[string]dtversion.DockerAuth{
			testKey: {
				Username: testName,
				Password: testValue,
			},
		},
	}
	return json.Marshal(dockerConf)
}
