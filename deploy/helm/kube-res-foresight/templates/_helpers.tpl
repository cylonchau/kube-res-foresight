{{- define "kube-res-foresight.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "kube-res-foresight.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- define "kube-res-foresight.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{ include "kube-res-foresight.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{- define "kube-res-foresight.selectorLabels" -}}
app.kubernetes.io/name: {{ include "kube-res-foresight.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "kube-res-foresight.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "kube-res-foresight.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{- define "kube-res-foresight.image" -}}
{{- if not .Values.image.repository -}}
{{- fail "image.repository is required: there is no public image, build deploy/docker/Dockerfile (target kube-res-foresight) and push it" -}}
{{- end -}}
{{- printf "%s:%s" .Values.image.repository (default .Chart.AppVersion .Values.image.tag) -}}
{{- end -}}

{{/* the database driver, checked */}}
{{- define "kube-res-foresight.driver" -}}
{{- $d := .Values.database.driver -}}
{{- if not (has $d (list "sqlite" "mysql" "postgres")) -}}
{{- fail (printf "database.driver must be sqlite, mysql or postgres, not %q" $d) -}}
{{- end -}}
{{- if and (eq $d "sqlite") (gt (int .Values.replicaCount) 1) -}}
{{- fail "sqlite is one file on one volume: use replicaCount 1, or mysql or postgres for more replicas" -}}
{{- end -}}
{{- if not (has .Values.mode (list "all" "split")) -}}
{{- fail (printf "mode must be all or split, not %q" .Values.mode) -}}
{{- end -}}
{{- if and (eq .Values.mode "split") (eq $d "sqlite") -}}
{{- fail "mode split runs the platform and the job engine in separate Pods: sqlite is one file on one volume, use mysql or postgres" -}}
{{- end -}}
{{- $d -}}
{{- end -}}

{{/* how the schema is made: job or initContainer */}}
{{- define "kube-res-foresight.migrationMode" -}}
{{- $m := .Values.migration.mode -}}
{{- if eq $m "auto" -}}
{{- if eq (include "kube-res-foresight.driver" .) "sqlite" -}}initContainer{{- else -}}job{{- end -}}
{{- else if has $m (list "job" "initContainer") -}}
{{- $m -}}
{{- else -}}
{{- fail (printf "migration.mode must be auto, job or initContainer, not %q" $m) -}}
{{- end -}}
{{- end -}}

{{- define "kube-res-foresight.secretName" -}}
{{- if .Values.secrets.existingSecret -}}{{ .Values.secrets.existingSecret }}{{- else -}}{{ include "kube-res-foresight.fullname" . }}{{- end -}}
{{- end -}}

{{/* the config file: nothing secret in it, the passwords come from the environment */}}
{{- define "kube-res-foresight.config" -}}
{{- $driver := include "kube-res-foresight.driver" . -}}
{{- $db := .Values.database -}}
{{- $cfg := dict "app_name" "kube-res-foresight" "database_driver" $driver "server" (dict "port" .Values.server.port "cors_allowed_origins" .Values.server.corsAllowedOrigins) "metrics" (dict "listen" (printf ":%d" (int .Values.metrics.port)) "legacy_names" (.Values.metrics.legacyNames | default false)) "loggers" (dict "business" (dict "enabled" true "level" "info" "format" "json" "outputs" (list (dict "type" "stdout"))) "sql" (dict "enabled" true "level" "warn" "format" "json" "outputs" (list (dict "type" "stdout")))) -}}
{{- if eq $driver "sqlite" -}}
{{- $_ := set $cfg "sqlite" (dict "file" $db.sqlite.file "max_open_connection" "20" "max_idle_connection" "10") -}}
{{- else if eq $driver "mysql" -}}
{{- if not $db.host -}}{{- fail "database.host is required with mysql" -}}{{- end -}}
{{- $_ := set $cfg "mysql" (dict "host" $db.host "port" (int (default 3306 $db.port)) "database" $db.name "username" $db.username "password" "" "max_open_connection" $db.maxOpenConnection "max_idle_connection" $db.maxIdleConnection) -}}
{{- else -}}
{{- if not $db.host -}}{{- fail "database.host is required with postgres" -}}{{- end -}}
{{- $_ := set $cfg "database" (dict "host" $db.host "port" (int (default 5432 $db.port)) "database" $db.name "username" $db.username "password" "" "sslmode" $db.sslMode) -}}
{{- end -}}
{{- toYaml (mergeOverwrite $cfg .Values.extraConfig) -}}
{{- end -}}

{{/* the environment: passwords and keys from the Secret */}}
{{- define "kube-res-foresight.env" -}}
{{- $driver := include "kube-res-foresight.driver" . -}}
{{- $secret := include "kube-res-foresight.secretName" . -}}
- name: KUBE_RES_FORESIGHT_SERVER_JWT_SECRET
  valueFrom:
    secretKeyRef:
      name: {{ $secret }}
      key: {{ .Values.secrets.keys.jwtSecret }}
- name: KUBE_RES_FORESIGHT_SECRET_KEY
  valueFrom:
    secretKeyRef:
      name: {{ $secret }}
      key: {{ .Values.secrets.keys.secretKey }}
{{- if eq $driver "mysql" }}
- name: KUBE_RES_FORESIGHT_MYSQL_PASSWORD
  valueFrom:
    secretKeyRef:
      name: {{ $secret }}
      key: {{ .Values.secrets.keys.databasePassword }}
{{- else if eq $driver "postgres" }}
- name: KUBE_RES_FORESIGHT_DATABASE_PASSWORD
  valueFrom:
    secretKeyRef:
      name: {{ $secret }}
      key: {{ .Values.secrets.keys.databasePassword }}
{{- end }}
{{- if .Values.timezone }}
- name: KUBE_RES_FORESIGHT_TIMEZONE
  value: {{ .Values.timezone | quote }}
{{- end }}
{{- with .Values.extraEnv }}
{{ toYaml . }}
{{- end }}
{{- end -}}

{{/* the job engine Pods of mode split: another name, so the Service and the PodDisruptionBudget of the platform do not select them */}}
{{- define "kube-res-foresight.jobName" -}}
{{- printf "%s-job" (include "kube-res-foresight.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "kube-res-foresight.jobSelectorLabels" -}}
app.kubernetes.io/name: {{ printf "%s-job" (include "kube-res-foresight.name" .) | trunc 63 | trimSuffix "-" }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}
