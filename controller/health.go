package controller

import (
	"fmt"

	"github.com/argoproj/gitops-engine/pkg/health"
	hookutil "github.com/argoproj/gitops-engine/pkg/sync/hook"
	"github.com/argoproj/gitops-engine/pkg/sync/ignore"
	kubeutil "github.com/argoproj/gitops-engine/pkg/utils/kube"
	log "github.com/sirupsen/logrus"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/argoproj/argo-cd/v3/common"
	"github.com/argoproj/argo-cd/v3/pkg/apis/application"
	appv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/argoproj/argo-cd/v3/util/lua"
)

// setApplicationHealth updates the health statuses of all resources performed in the comparison
func setApplicationHealth(resources []managedResource, statuses []appv1.ResourceStatus, resourceOverrides map[string]appv1.ResourceOverride, app *appv1.Application, persistResourceHealth bool) (*appv1.HealthStatus, error) {
	var savedErr error
	var errCount uint

	appHealth := appv1.HealthStatus{Status: health.HealthStatusHealthy}
	for i, res := range resources {
		if res.Target != nil && hookutil.Skip(res.Target) {
			continue
		}
		if res.Target != nil && res.Target.GetAnnotations() != nil && res.Target.GetAnnotations()[common.AnnotationIgnoreHealthCheck] == "true" {
			continue
		}

		if res.Live != nil && (hookutil.IsHook(res.Live) || ignore.Ignore(res.Live)) {
			continue
		}

		var healthStatus *health.HealthStatus
		var err error
		healthOverrides := lua.ResourceHealthOverrides(resourceOverrides)
		gvk := schema.GroupVersionKind{Group: res.Group, Version: res.Version, Kind: res.Kind}
		if res.Live == nil {
			healthStatus = &health.HealthStatus{Status: health.HealthStatusMissing}
		} else {
			// App the manages itself should not affect own health
			if isSelfReferencedApp(app, kubeutil.GetObjectRef(res.Live)) {
				continue
			}
			healthStatus, err = health.GetResourceHealth(res.Live, healthOverrides)
			if err != nil && savedErr == nil {
				errCount++
				savedErr = fmt.Errorf("failed to get resource health for %q with name %q in namespace %q: %w", res.Live.GetKind(), res.Live.GetName(), res.Live.GetNamespace(), err)
				// also log so we don't lose the message
				log.WithField("application", app.QualifiedName()).Warn(savedErr)
			}
		}

		if healthStatus == nil {
			continue
		}

		if persistResourceHealth {
			resHealth := appv1.HealthStatus{Status: healthStatus.Status, Message: healthStatus.Message}
			statuses[i].Health = &resHealth
		} else {
			statuses[i].Health = nil
		}

		// Is health status is missing but resource has not built-in/custom health check then it should not affect parent app health
		if _, hasOverride := healthOverrides[lua.GetConfigMapKey(gvk)]; healthStatus.Status == health.HealthStatusMissing && !hasOverride && health.GetHealthCheckFunc(gvk) == nil {
			continue
		}

		// Missing or Unknown health status of child Argo CD app should not affect parent
		if res.Kind == application.ApplicationKind && res.Group == application.Group && (healthStatus.Status == health.HealthStatusMissing || healthStatus.Status == health.HealthStatusUnknown) {
			continue
		}

		if health.IsWorse(appHealth.Status, healthStatus.Status) {
			appHealth.Status = healthStatus.Status
		}
	}
	// if the status didn't change, don't update the timestamp
	if app.Status.Health.Status == appHealth.Status && app.Status.Health.LastTransitionTime != nil {
		appHealth.LastTransitionTime = app.Status.Health.LastTransitionTime
	} else {
		now := metav1.Now()
		appHealth.LastTransitionTime = &now
	}
	if persistResourceHealth {
		app.Status.ResourceHealthSource = appv1.ResourceHealthLocationInline
	} else {
		app.Status.ResourceHealthSource = appv1.ResourceHealthLocationAppTree
	}
	if savedErr != nil && errCount > 1 {
		savedErr = fmt.Errorf("see application-controller logs for %d other errors; most recent error was: %w", errCount-1, savedErr)
	}
	return &appHealth, savedErr
}
