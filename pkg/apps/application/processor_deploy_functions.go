// Copyright 2022 The kubegems.io Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package application

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/api/autoscaling/v2beta2"
	batchv1 "k8s.io/api/batch/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"kubegems.io/kubegems/pkg/service/handlers/noproxy"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

func (p *ApplicationProcessor) GetHorizontalPodAutoscaler(ctx context.Context, ref PathRef) (*v2beta2.HorizontalPodAutoscaler, error) {
	var ret *v2beta2.HorizontalPodAutoscaler

	err := p.Manifest.StoreFunc(ctx, ref, func(ctx context.Context, store GitStore) error {
		workload, err := ParseMainWorkload(ctx, store)
		if err != nil {
			return err
		}
		// type check
		switch workload.(type) {
		case *appsv1.Deployment:
		case *appsv1.StatefulSet:
		case *batchv1.Job:
		default:
			return fmt.Errorf("unsupported workload type %s", workload.GetObjectKind().GroupVersionKind())
		}

		sc := &v2beta2.HorizontalPodAutoscaler{
			ObjectMeta: metav1.ObjectMeta{
				Name:      noproxy.FormatHPAName(workload.GetObjectKind().GroupVersionKind().Kind, workload.GetName()),
				Namespace: workload.GetNamespace(),
			},
		}
		if err := store.Get(ctx, client.ObjectKeyFromObject(sc), sc); err != nil {
			return err
		}
		ret = sc
		return nil
	})
	if err != nil {
		return nil, err
	}
	return ret, nil
}

func (p *ApplicationProcessor) DeleteHorizontalPodAutoscaler(ctx context.Context, ref PathRef) error {
	updatefun := func(ctx context.Context, store GitStore) error {
		hpalist := &v2beta2.HorizontalPodAutoscalerList{}
		if err := store.List(ctx, hpalist); err != nil {
			return err
		}
		for _, v := range hpalist.Items {
			_ = store.Delete(ctx, &v)
		}
		return nil
	}
	return p.Manifest.StoreUpdateFunc(ctx, ref, updatefun, "remove hpa")
}

func (p *ApplicationProcessor) SetHorizontalPodAutoscaler(ctx context.Context, ref PathRef, scalerMetrics HPAMetrics) error {
	updatefun := func(_ context.Context, store GitStore) error {
		workload, err := ParseMainWorkload(ctx, store)
		if err != nil {
			return err
		}
		// type check
		switch workload.(type) {
		case *appsv1.Deployment:
		case *appsv1.StatefulSet:
		case *batchv1.Job:
		default:
			return fmt.Errorf("unsupported workload type %s", workload.GetObjectKind().GroupVersionKind())
		}

		name := workload.GetName()
		namespace := workload.GetNamespace()
		gv := appsv1.SchemeGroupVersion
		kind := workload.GetObjectKind().GroupVersionKind().Kind

		sc := &v2beta2.HorizontalPodAutoscaler{
			ObjectMeta: metav1.ObjectMeta{
				Name:      noproxy.FormatHPAName(kind, name),
				Namespace: namespace,
			},
		}
		scalerSpec := v2beta2.HorizontalPodAutoscalerSpec{
			MinReplicas: scalerMetrics.MinReplicas,
			MaxReplicas: scalerMetrics.MaxReplicas,
			ScaleTargetRef: v2beta2.CrossVersionObjectReference{
				Kind:       kind,
				Name:       name,
				APIVersion: gv.Identifier(),
			},
			Metrics: func() []v2beta2.MetricSpec {
				var metrics []v2beta2.MetricSpec
				if scalerMetrics.Cpu > 0 {
					metrics = append(metrics, v2beta2.MetricSpec{
						Type: v2beta2.ResourceMetricSourceType,
						Resource: &v2beta2.ResourceMetricSource{
							Name: v1.ResourceCPU,
							Target: v2beta2.MetricTarget{
								Type:               v2beta2.UtilizationMetricType,
								AverageUtilization: &scalerMetrics.Cpu,
							},
						},
					})
				}
				if scalerMetrics.Memory > 0 {
					metrics = append(metrics, v2beta2.MetricSpec{
						Type: v2beta2.ResourceMetricSourceType,
						Resource: &v2beta2.ResourceMetricSource{
							Name: v1.ResourceMemory,
							Target: v2beta2.MetricTarget{
								Type:               v2beta2.UtilizationMetricType,
								AverageUtilization: &scalerMetrics.Memory,
							},
						},
					})
				}
				return metrics
			}(),
		}
		_, err = controllerutil.CreateOrUpdate(ctx, store, sc, func() error {
			// update spec
			sc.Spec = scalerSpec
			return nil
		})
		if err != nil {
			return err
		}
		return nil
	}
	return p.Manifest.StoreUpdateFunc(ctx, ref, updatefun, "update hpa")
}

func (p *ApplicationProcessor) GetReplicas(ctx context.Context, ref PathRef) (*int32, error) {
	var replicas *int32
	_ = p.Manifest.StoreFunc(ctx, ref, func(ctx context.Context, store GitStore) error {
		workload, _ := ParseMainWorkload(ctx, store)
		switch app := workload.(type) {
		case *appsv1.Deployment:
			replicas = app.Spec.Replicas
		case *appsv1.StatefulSet:
			replicas = app.Spec.Replicas
		}
		return nil
	})
	return replicas, nil
}

func (p *ApplicationProcessor) SetReplicas(ctx context.Context, ref PathRef, replicas *int32) error {
	updatefunc := func(ctx context.Context, store GitStore) error {
		workload, err := ParseMainWorkload(ctx, store)
		if err != nil {
			return err
		}
		switch app := workload.(type) {
		case *appsv1.Deployment:
			app.Spec.Replicas = replicas
			return store.Update(ctx, app)
		case *appsv1.StatefulSet:
			app.Spec.Replicas = replicas
			return store.Update(ctx, app)
		default:
			return fmt.Errorf("unsupported scale workload: %s", workload.GetResourceVersion())
		}
	}
	return p.Manifest.StoreUpdateFunc(ctx, ref, updatefunc, fmt.Sprintf("scale replicas to %v", replicas))
}
