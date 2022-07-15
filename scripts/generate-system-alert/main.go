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

package main

import (
	"bytes"
	"io"
	"os"
	"regexp"

	"kubegems.io/kubegems/pkg/apis/gems"
	"kubegems.io/kubegems/pkg/utils/prometheus"
	"sigs.k8s.io/yaml"
)

const (
	nstmpl = "{{ .Release.Namespace }}"
)

var (
	agentURL = `{{ index .Values "kubegems-local" "alert" "address" }}`
)

func main() {
	alerts := []prometheus.MonitorAlertRule{}
	file, err := os.Open("scripts/generate-system-alert/system-alert.yaml")
	if err != nil {
		panic(err)
	}
	bts, err := io.ReadAll(file)
	if err != nil {
		panic(err)
	}
	if err := yaml.Unmarshal(bts, &alerts); err != nil {
		panic(err)
	}

	raw := &prometheus.RawMonitorAlertResource{
		Base: &prometheus.BaseAlertResource{
			AMConfig: prometheus.GetBaseAlertmanagerConfig(gems.NamespaceMonitor, prometheus.MonitorAlertCRDName),
		},
		PrometheusRule: prometheus.GetBasePrometheusRule(gems.NamespaceMonitor, prometheus.MonitorAlertCRDName),
		MonitorOptions: prometheus.DefaultMonitorOptions(),
	}

	for _, alert := range alerts {
		alert.Source = prometheus.MonitorAlertCRDName
		if err := alert.CheckAndModify(raw.MonitorOptions); err != nil {
			panic(err)
		}
		if err := raw.ModifyAlertRule(alert, prometheus.Add); err != nil {
			panic(err)
		}
	}

	raw.Base.AMConfig.Spec.Receivers[1].WebhookConfigs[0].URL = &agentURL
	raw.Base.AMConfig.Annotations = map[string]string{
		"bundle.kubegems.io/ignore-options": "OnUpdate",
	}
	raw.PrometheusRule.Annotations = map[string]string{
		"bundle.kubegems.io/ignore-options": "OnUpdate",
	}

	if err := os.WriteFile("deploy/plugins/monitoring/templates/kubegems-default-monitor-amconfig.yaml", getOutput(raw.Base.AMConfig), 0644); err != nil {
		panic(err)
	}
	if err := os.WriteFile("deploy/plugins/monitoring/templates/kubegems-default-monitor-promrule.yaml", getOutput(raw.PrometheusRule), 0644); err != nil {
		panic(err)
	}
}

var reg = regexp.MustCompile("{{ %")

func getOutput(obj interface{}) []byte {
	bts, _ := yaml.Marshal(obj)
	// 对不需要替换的'{{`', '}}'转义，https://stackoverflow.com/questions/17641887/how-do-i-escape-and-delimiters-in-go-templates

	bts = bytes.ReplaceAll(bts, []byte(":{{"), []byte(`:{{"{{"}}`))
	// bts = bytes.ReplaceAll(bts, []byte("}}]"), []byte(`{{"}}"}}]`))
	bts = bytes.ReplaceAll(bts, []byte("{{ $value"), []byte(`{{"{{ $value"}}`))
	bts = bytes.ReplaceAll(bts, []byte(gems.NamespaceMonitor), []byte(nstmpl))
	buf := bytes.NewBuffer([]byte{})
	buf.WriteString("# This file is auto generated by 'make generate', please do not modify it manually.\n")
	buf.Write(bts)
	return buf.Bytes()
}
