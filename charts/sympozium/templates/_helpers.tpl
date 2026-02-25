{{/*
Expand the name of the chart.
*/}}
{{- define "sympozium.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "sympozium.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Chart label helper.
*/}}
{{- define "sympozium.labels" -}}
helm.sh/chart: {{ include "sympozium.chart" . }}
{{ include "sympozium.selectorLabels" . }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: sympozium
{{- end }}

{{/*
Selector labels.
*/}}
{{- define "sympozium.selectorLabels" -}}
app.kubernetes.io/name: {{ include "sympozium.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Chart name and version.
*/}}
{{- define "sympozium.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Image tag helper — defaults to Chart.AppVersion.
*/}}
{{- define "sympozium.imageTag" -}}
{{- .Values.image.tag | default (printf "v%s" .Chart.AppVersion) }}
{{- end }}

{{/*
Controller image.
*/}}
{{- define "sympozium.controllerImage" -}}
{{- $repo := .Values.controller.image.repository | default (printf "%s/controller" .Values.image.registry) }}
{{- $tag := .Values.controller.image.tag | default (include "sympozium.imageTag" .) }}
{{- printf "%s:%s" $repo $tag }}
{{- end }}

{{/*
API server image.
*/}}
{{- define "sympozium.apiserverImage" -}}
{{- $repo := .Values.apiserver.image.repository | default (printf "%s/apiserver" .Values.image.registry) }}
{{- $tag := .Values.apiserver.image.tag | default (include "sympozium.imageTag" .) }}
{{- printf "%s:%s" $repo $tag }}
{{- end }}

{{/*
Webhook image.
*/}}
{{- define "sympozium.webhookImage" -}}
{{- $repo := .Values.webhook.image.repository | default (printf "%s/webhook" .Values.image.registry) }}
{{- $tag := .Values.webhook.image.tag | default (include "sympozium.imageTag" .) }}
{{- printf "%s:%s" $repo $tag }}
{{- end }}

{{/*
NATS URL — internal or external.
*/}}
{{- define "sympozium.natsUrl" -}}
{{- if .Values.nats.enabled }}
{{- printf "nats://nats.%s.svc:4222" .Values.namespace }}
{{- else }}
{{- .Values.nats.externalUrl }}
{{- end }}
{{- end }}

{{/*
Namespace helper.
*/}}
{{- define "sympozium.namespace" -}}
{{- .Values.namespace | default "sympozium-system" }}
{{- end }}
