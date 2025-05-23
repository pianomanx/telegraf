package kube_inventory

import (
	"context"
	"strings"

	corev1 "k8s.io/api/core/v1"

	"github.com/influxdata/telegraf"
)

func collectPods(ctx context.Context, acc telegraf.Accumulator, ki *KubernetesInventory) {
	var list corev1.PodList
	listRef := &list
	var err error

	if ki.KubeletURL != "" {
		err = ki.queryPodsFromKubelet(ki.KubeletURL+"/pods", listRef)
	} else {
		listRef, err = ki.client.getPods(ctx, ki.NodeName)
	}

	if err != nil {
		acc.AddError(err)
		return
	}
	for i := range listRef.Items {
		ki.gatherPod(&listRef.Items[i], acc)
	}
}

func (ki *KubernetesInventory) gatherPod(p *corev1.Pod, acc telegraf.Accumulator) {
	creationTS := p.GetCreationTimestamp()
	if creationTS.IsZero() {
		return
	}

	containerList := make(map[string]*corev1.ContainerStatus, len(p.Status.ContainerStatuses))
	for i := range p.Status.ContainerStatuses {
		containerList[p.Status.ContainerStatuses[i].Name] = &p.Status.ContainerStatuses[i]
	}

	for _, c := range p.Spec.Containers {
		cs, ok := containerList[c.Name]
		if !ok {
			cs = &corev1.ContainerStatus{}
		}
		ki.gatherPodContainer(p, *cs, c, acc)
	}
}

func (ki *KubernetesInventory) gatherPodContainer(p *corev1.Pod, cs corev1.ContainerStatus, c corev1.Container, acc telegraf.Accumulator) {
	stateCode := 3
	stateReason := ""
	state := "unknown"
	readiness := "unready"

	switch {
	case cs.State.Running != nil:
		stateCode = 0
		state = "running"
	case cs.State.Terminated != nil:
		stateCode = 1
		state = "terminated"
		stateReason = cs.State.Terminated.Reason
	case cs.State.Waiting != nil:
		stateCode = 2
		state = "waiting"
		stateReason = cs.State.Waiting.Reason
	}

	if cs.Ready {
		readiness = "ready"
	}

	fields := map[string]interface{}{
		"restarts_total": cs.RestartCount,
		"state_code":     stateCode,
	}

	// deprecated in 1.15: use `state_reason` instead
	if state == "terminated" {
		fields["terminated_reason"] = stateReason
	}

	if stateReason != "" {
		fields["state_reason"] = stateReason
	}

	phaseReason := p.Status.Reason
	if phaseReason != "" {
		fields["phase_reason"] = phaseReason
	}

	tags := map[string]string{
		"container_name": c.Name,
		"namespace":      p.Namespace,
		"node_name":      p.Spec.NodeName,
		"pod_name":       p.Name,
		"phase":          string(p.Status.Phase),
		"state":          state,
		"readiness":      readiness,
	}
	splitImage := strings.Split(c.Image, ":")
	if len(splitImage) == 2 {
		tags["version"] = splitImage[1]
	}
	tags["image"] = splitImage[0]
	for key, val := range p.Spec.NodeSelector {
		if ki.selectorFilter.Match(key) {
			tags["node_selector_"+key] = val
		}
	}

	req := c.Resources.Requests
	lim := c.Resources.Limits

	for resourceName, val := range req {
		switch resourceName {
		case "cpu":
			fields["resource_requests_millicpu_units"] = ki.convertQuantity(val.String(), 1000)
		case "memory":
			fields["resource_requests_memory_bytes"] = ki.convertQuantity(val.String(), 1)
		}
	}
	for resourceName, val := range lim {
		switch resourceName {
		case "cpu":
			fields["resource_limits_millicpu_units"] = ki.convertQuantity(val.String(), 1000)
		case "memory":
			fields["resource_limits_memory_bytes"] = ki.convertQuantity(val.String(), 1)
		}
	}

	for _, val := range p.Status.Conditions {
		conditiontags := map[string]string{
			"container_name": c.Name,
			"image":          splitImage[0],
			"status":         string(val.Status),
			"namespace":      p.Namespace,
			"node_name":      p.Spec.NodeName,
			"pod_name":       p.Name,
			"condition":      string(val.Type),
		}
		if len(splitImage) == 2 {
			conditiontags["version"] = splitImage[1]
		}
		running := 0
		podready := 0
		if val.Status == "True" {
			if val.Type == "Ready" {
				podready = 1
			}
			running = 1
		} else if val.Status == "Unknown" {
			if val.Type == "Ready" {
				podready = 0
			}
			running = 2
		}
		conditionfields := map[string]interface{}{
			"status_condition": running,
			"ready":            podready,
		}
		acc.AddFields(podContainerMeasurement, conditionfields, conditiontags)
	}

	acc.AddFields(podContainerMeasurement, fields, tags)
}
