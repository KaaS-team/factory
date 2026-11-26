{{- define "factory.image" -}}
{{ .Values.image.repository }}:{{ .Values.image.tag | default .Chart.AppVersion }}
{{- end -}}
{{- define "factory.sa" -}}
{{ if .Values.serviceAccount.create }}{{ .Values.serviceAccount.name | default (printf "%s-factory" .Release.Name) }}{{ else }}{{ .Values.serviceAccount.name | default "default" }}{{ end }}
{{- end -}}
{{- define "factory.planeURL" -}}
{{ .Values.planeURL | default (printf "http://%s-plane:%v" .Release.Name .Values.controlPlane.port) }}
{{- end -}}
