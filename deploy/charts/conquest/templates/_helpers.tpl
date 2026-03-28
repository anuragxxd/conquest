{{/*
Expand the name of the chart.
*/}}
{{- define "conquest.fullname" -}}
{{- default "conquest" .Values.conquest.appname | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Build the in-cluster base URL that runner Jobs use to call back into
the Conquest service.
*/}}
{{- define "conquest.internalBaseURL" -}}
{{- if .Values.conquest.internalBaseURLOverride -}}
{{- .Values.conquest.internalBaseURLOverride -}}
{{- else -}}
{{- $host := printf "http://%s.%s.svc" (include "conquest.fullname" .) .Release.Namespace -}}
{{- if eq (int .Values.conquest.service.port) 80 -}}
{{- $host -}}
{{- else -}}
{{- printf "%s:%v" $host .Values.conquest.service.port -}}
{{- end -}}
{{- end -}}
{{- end }}

{{/*
Create chart name and version as used by the chart label.
*/}}
{{- define "conquest.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels.
*/}}
{{- define "conquest.labels" -}}
helm.sh/chart: {{ include "conquest.chart" . }}
{{ include "conquest.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels.
*/}}
{{- define "conquest.selectorLabels" -}}
app.kubernetes.io/name: {{ include "conquest.fullname" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Service account name.
*/}}
{{- define "conquest.serviceAccountName" -}}
{{- if .Values.conquest.serviceAccount.create }}
{{- default (include "conquest.fullname" .) .Values.conquest.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.conquest.serviceAccount.name }}
{{- end }}
{{- end }}
