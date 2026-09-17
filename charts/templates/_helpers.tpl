{{/*
Expand the name of the chart.
*/}}
{{- define "kflared.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create the name of the Cloudflare API token Secret.
*/}}
{{- define "kflared.secretName" -}}
{{- .Values.externalSecrets.targetSecretName }}
{{- end }}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "kflared.fullname" -}}
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
Create chart name and version as used by the chart label.
*/}}
{{- define "kflared.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels.
*/}}
{{- define "kflared.labels" -}}
helm.sh/chart: {{ include "kflared.chart" . }}
{{ include "kflared.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels.
*/}}
{{- define "kflared.selectorLabels" -}}
app.kubernetes.io/name: {{ include "kflared.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/component: {{ .Values.kubernetesComponent }}
{{- end }}

{{/*
Create the name of the service account to use.
*/}}
{{- define "kflared.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "kflared.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- required "serviceAccount.name is required when serviceAccount.create=false" .Values.serviceAccount.name }}
{{- end }}
{{- end }}
