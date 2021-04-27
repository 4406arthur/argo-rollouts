package rollout

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	patchtypes "k8s.io/apimachinery/pkg/types"

	"github.com/argoproj/argo-rollouts/pkg/apis/rollouts/v1alpha1"
	"github.com/argoproj/argo-rollouts/utils/annotations"
	replicasetutil "github.com/argoproj/argo-rollouts/utils/replicaset"
	serviceutil "github.com/argoproj/argo-rollouts/utils/service"
)

const (
	switchSelectorPatch = `{
	"spec": {
		"selector": {
			"` + v1alpha1.DefaultRolloutUniqueLabelKey + `": "%s"
		}
	}
}`
	switchSelectorAndAddManagedByPatch = `{
	"metadata": {
		"annotations": {
			"` + v1alpha1.ManagedByRolloutsKey + `": "%s"
		}
	},
	"spec": {
		"selector": {
			"` + v1alpha1.DefaultRolloutUniqueLabelKey + `": "%s"
		}
	}
}`
	enableMirrirPatch = `{
	"metadata": {
		"annotations": {
			"nginx.ingress.kubernetes.io/mirror-target": "%s"
		}
	}
}`
	disabledMirrorPatch = `{
	"metadata": {
		"annotations": {
			"nginx.ingress.kubernetes.io/mirror-target": null
		}
	}
}`
)

func generatePatch(service *corev1.Service, newRolloutUniqueLabelValue string, r *v1alpha1.Rollout) string {
	if _, ok := service.Annotations[v1alpha1.ManagedByRolloutsKey]; !ok {
		return fmt.Sprintf(switchSelectorAndAddManagedByPatch, r.Name, newRolloutUniqueLabelValue)
	}
	return fmt.Sprintf(switchSelectorPatch, newRolloutUniqueLabelValue)
}

func generateServieFQDN(service *corev1.Service) string {
	return service.GetName() + "." + service.Namespace + ".svc.cluster.local"
}

// switchSelector switch the selector on an existing service to a new value
func (c rolloutContext) switchServiceSelector(service *corev1.Service, newRolloutUniqueLabelValue string, r *v1alpha1.Rollout) error {
	ctx := context.TODO()
	if service.Spec.Selector == nil {
		service.Spec.Selector = make(map[string]string)
	}
	_, hasManagedRollout := serviceutil.HasManagedByAnnotation(service)
	oldPodHash, ok := service.Spec.Selector[v1alpha1.DefaultRolloutUniqueLabelKey]
	if ok && oldPodHash == newRolloutUniqueLabelValue && hasManagedRollout {
		return nil
	}
	patch := generatePatch(service, newRolloutUniqueLabelValue, r)
	_, err := c.kubeclientset.CoreV1().Services(service.Namespace).Patch(ctx, service.Name, patchtypes.StrategicMergePatchType, []byte(patch), metav1.PatchOptions{})
	if err != nil {
		return err
	}
	msg := fmt.Sprintf("Switched selector for service '%s' from '%s' to '%s'", service.Name, oldPodHash, newRolloutUniqueLabelValue)
	c.log.Info(msg)
	c.recorder.Event(r, corev1.EventTypeNormal, "SwitchService", msg)
	service.Spec.Selector[v1alpha1.DefaultRolloutUniqueLabelKey] = newRolloutUniqueLabelValue
	return err
}

// switchSelectorV2 switch the selector on an existing service to a new value and add mirror feature
func (c rolloutContext) switchServiceSelectorV2(service *corev1.Service, newRolloutUniqueLabelValue string, r *v1alpha1.Rollout, mirrorCallback func()) error {
	ctx := context.TODO()
	if service.Spec.Selector == nil {
		service.Spec.Selector = make(map[string]string)
	}
	_, hasManagedRollout := serviceutil.HasManagedByAnnotation(service)
	oldPodHash, ok := service.Spec.Selector[v1alpha1.DefaultRolloutUniqueLabelKey]
	if ok && oldPodHash == newRolloutUniqueLabelValue && hasManagedRollout {
		return nil
	}
	patch := generatePatch(service, newRolloutUniqueLabelValue, r)
	_, err := c.kubeclientset.CoreV1().Services(service.Namespace).Patch(ctx, service.Name, patchtypes.StrategicMergePatchType, []byte(patch), metav1.PatchOptions{})
	if err != nil {
		return err
	}
	if oldPodHash != "" {
		// do not need handle error, should not affect running service
		mirrorCallback()
	}
	msg := fmt.Sprintf("Switched selector for service '%s' from '%s' to '%s'", service.Name, oldPodHash, newRolloutUniqueLabelValue)
	c.log.Info(msg)
	c.recorder.Event(r, corev1.EventTypeNormal, "SwitchService", msg)
	service.Spec.Selector[v1alpha1.DefaultRolloutUniqueLabelKey] = newRolloutUniqueLabelValue
	return err
}

func (c *rolloutContext) reconcilePreviewService(previewSvc *corev1.Service) error {
	if previewSvc == nil {
		return nil
	}
	newPodHash := c.newRS.Labels[v1alpha1.DefaultRolloutUniqueLabelKey]

	// traffic mirror extend case, change to invoke switchServiceSelectorV2
	if c.rollout.Spec.Strategy.BlueGreen.Mirror != nil && *c.rollout.Spec.Strategy.BlueGreen.Mirror {
		msg := fmt.Sprintf("16916212 mirror entry %s", c.rollout.Spec.Strategy.BlueGreen.ActiveService)
		c.log.Info(msg)
		mirrorEnable := func() {
			c.log.Info("188754781584 god job here")
			mirrorPatch := fmt.Sprintf(enableMirrirPatch, generateServieFQDN(previewSvc)+"$request_uri")
			targetIngress, err := c.ingressesLister.Ingresses(c.rollout.Namespace).Get(c.rollout.Spec.Strategy.BlueGreen.Ingress)
			if err != nil {
				c.log.Warningf("12659192 cannot get target ingress: %s", err.Error())
				return
			}
			ctx := context.TODO()
			_, err = c.kubeclientset.NetworkingV1beta1().Ingresses(targetIngress.Namespace).Patch(
				ctx, targetIngress.Name, patchtypes.StrategicMergePatchType, []byte(mirrorPatch), metav1.PatchOptions{})
			if err != nil {
				c.log.Warningf("12741051 mirror enable error: %s", err.Error())
				return
			}
		}

		err := c.switchServiceSelectorV2(previewSvc, newPodHash, c.rollout, mirrorEnable)
		if err != nil {
			return err
		}

	} else {
		err := c.switchServiceSelector(previewSvc, newPodHash, c.rollout)
		if err != nil {
			return err
		}
	}

	return nil
}

func (c *rolloutContext) reconcileActiveService(activeSvc *corev1.Service) error {
	if !replicasetutil.ReadyForPause(c.rollout, c.newRS, c.allRSs) || !annotations.IsSaturated(c.rollout, c.newRS) {
		c.log.Infof("skipping active service switch: New RS '%s' is not fully saturated", c.newRS.Name)
		return nil
	}

	newPodHash := activeSvc.Spec.Selector[v1alpha1.DefaultRolloutUniqueLabelKey]
	if c.isBlueGreenFastTracked(activeSvc) {
		newPodHash = c.newRS.Labels[v1alpha1.DefaultRolloutUniqueLabelKey]
	}
	if c.pauseContext.CompletedBlueGreenPause() && c.completedPrePromotionAnalysis() {
		newPodHash = c.newRS.Labels[v1alpha1.DefaultRolloutUniqueLabelKey]
	}

	if c.rollout.Status.Abort {
		newPodHash = c.rollout.Status.StableRS
	}

	// traffic mirror extend case, change to invoke switchServiceSelectorV2
	if c.rollout.Spec.Strategy.BlueGreen.Mirror != nil && *c.rollout.Spec.Strategy.BlueGreen.Mirror {
		msg := fmt.Sprintf("689169411 mirror entry %s", c.rollout.Spec.Strategy.BlueGreen.ActiveService)
		c.log.Info(msg)
		mirrorDisabeled := func() {
			c.log.Info("8374968212 god job here")
			targetIngress, err := c.ingressesLister.Ingresses(c.rollout.Namespace).Get(c.rollout.Spec.Strategy.BlueGreen.Ingress)
			if err != nil {
				c.log.Warningf("28752921 cannot get target ingress: %s", err.Error())
				return
			}
			ctx := context.TODO()
			_, err = c.kubeclientset.NetworkingV1beta1().Ingresses(targetIngress.Namespace).Patch(
				ctx, targetIngress.Name, patchtypes.StrategicMergePatchType, []byte(disabledMirrorPatch), metav1.PatchOptions{})
			if err != nil {
				c.log.Warningf("5892359 mirror disabled error: %s", err.Error())
				return
			}
		}

		err := c.switchServiceSelectorV2(activeSvc, newPodHash, c.rollout, mirrorDisabeled)
		if err != nil {
			return err
		}

	} else {
		err := c.switchServiceSelector(activeSvc, newPodHash, c.rollout)
		if err != nil {
			return err
		}
	}
	return nil
}

func (c *rolloutContext) getPreviewAndActiveServices() (*corev1.Service, *corev1.Service, error) {
	var previewSvc *corev1.Service
	var activeSvc *corev1.Service
	var err error

	if c.rollout.Spec.Strategy.BlueGreen.PreviewService != "" {
		previewSvc, err = c.servicesLister.Services(c.rollout.Namespace).Get(c.rollout.Spec.Strategy.BlueGreen.PreviewService)
		if err != nil {
			return nil, nil, err
		}
	}
	activeSvc, err = c.servicesLister.Services(c.rollout.Namespace).Get(c.rollout.Spec.Strategy.BlueGreen.ActiveService)
	if err != nil {
		return nil, nil, err
	}
	return previewSvc, activeSvc, nil
}

func (c *rolloutContext) reconcileStableAndCanaryService() error {
	if c.rollout.Spec.Strategy.Canary == nil {
		return nil
	}
	err := c.ensureSVCTargets(c.rollout.Spec.Strategy.Canary.StableService, c.stableRS)
	if err != nil {
		return err
	}

	if c.newRSReady() {
		err = c.ensureSVCTargets(c.rollout.Spec.Strategy.Canary.CanaryService, c.newRS)
		if err != nil {
			return err
		}
	}
	return nil
}

func (c *rolloutContext) ensureSVCTargets(svcName string, rs *appsv1.ReplicaSet) error {
	if rs == nil || svcName == "" {
		return nil
	}
	svc, err := c.servicesLister.Services(c.rollout.Namespace).Get(svcName)
	if err != nil {
		return err
	}
	if svc.Spec.Selector[v1alpha1.DefaultRolloutUniqueLabelKey] != rs.Labels[v1alpha1.DefaultRolloutUniqueLabelKey] {
		err = c.switchServiceSelector(svc, rs.Labels[v1alpha1.DefaultRolloutUniqueLabelKey], c.rollout)
		if err != nil {
			return err
		}
	}
	return nil
}

func (c *rolloutContext) newRSReady() bool {
	if c.newRS == nil {
		return false
	}
	replicas := c.newRS.Spec.Replicas
	readyReplicas := c.newRS.Status.ReadyReplicas
	return replicas != nil && *replicas != 0 && readyReplicas != 0 && *replicas <= readyReplicas
}
