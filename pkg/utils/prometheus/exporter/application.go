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

package exporter

import (
	"context"
	"strings"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	"k8s.io/apimachinery/pkg/labels"
	"kubegems.io/kubegems/pkg/apis/application"
	gemlabels "kubegems.io/kubegems/pkg/apis/gems"
	"kubegems.io/kubegems/pkg/log"
	"kubegems.io/kubegems/pkg/utils/argo"
)

type ApplicationCollector struct {
	projectInfo *prometheus.Desc

	*argo.Client
	mutex sync.Mutex
}

func NewApplicationCollector(cli *argo.Client) func(_ *log.Logger) (Collector, error) {
	return func(_ *log.Logger) (Collector, error) {
		return &ApplicationCollector{
			projectInfo: prometheus.NewDesc(
				prometheus.BuildFQName(getNamespace(), "application", "status"),
				"Gems application status",
				[]string{"application", "creator", "from", "environment", "project", "tenant", "cluster", "namespace", "status"},
				nil,
			),
			Client: cli,
		}, nil
	}
}

func (c *ApplicationCollector) Update(ch chan<- prometheus.Metric) error {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	apps, err := c.ListArgoApp(context.TODO(), labels.Everything())
	if err != nil {
		log.Errorf("faild to list apps: %v", err)
		return err
	}

	for _, v := range apps.Items {
		if v.Labels != nil && v.Labels[gemlabels.LabelApplication] != "" {
			ch <- prometheus.MustNewConstMetric(
				c.projectInfo,
				prometheus.GaugeValue,
				1,

				v.Labels[gemlabels.LabelApplication],
				v.Annotations[application.AnnotationCreator],
				v.Labels[application.LabelFrom],
				v.Labels[gemlabels.LabelEnvironment],
				v.Labels[gemlabels.LabelProject],
				v.Labels[gemlabels.LabelTenant],
				strings.TrimPrefix(v.Spec.Destination.Name, "argocd-cluster-"),
				v.Spec.Destination.Namespace,
				string(v.Status.Health.Status),
			)
		}
	}

	return nil
}
