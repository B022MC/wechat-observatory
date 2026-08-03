#!/usr/bin/env bash
set -Eeuo pipefail

umask 077

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
backup_dir=${PD_WECHAT_BACKUP_DIR:-"${script_dir}/backups/mysql"}
retention_days=${PD_WECHAT_BACKUP_RETENTION_DAYS:-15}

if ! [[ ${retention_days} =~ ^[1-9][0-9]*$ ]]; then
  echo "PD_WECHAT_BACKUP_RETENTION_DAYS must be a positive whole number" >&2
  exit 2
fi

mkdir -p -- "${backup_dir}"
timestamp=$(date -u +%Y%m%dT%H%M%SZ)
final_file="${backup_dir}/pd-wechat-${timestamp}.sql.gz"
temporary_file=$(mktemp "${backup_dir}/.pd-wechat-${timestamp}.XXXXXX.sql.gz")

cleanup() {
  if [[ -n ${temporary_file} ]]; then
    rm -f -- "${temporary_file}"
  fi
}
trap cleanup EXIT

compose=(docker compose --project-directory "${script_dir}" --env-file "${script_dir}/.env" -f "${script_dir}/docker-compose.yml")

"${compose[@]}" exec -T mysql sh -ceu '
  export MYSQL_PWD="$MYSQL_ROOT_PASSWORD"
  exec mysqldump \
    --single-transaction \
    --routines \
    --events \
    --triggers \
    --databases "$OBS_DB_NAME" "$GATEWAY_DB_NAME" \
    -uroot
' | gzip -c > "${temporary_file}"

test -s "${temporary_file}"
gzip -t "${temporary_file}"
mv -- "${temporary_file}" "${final_file}"
temporary_file=""

(
  cd -- "${backup_dir}"
  sha256sum "$(basename -- "${final_file}")" > "$(basename -- "${final_file}").sha256"
)

# Keep complete dump/checksum pairs for the most recent retention window.
find "${backup_dir}" -maxdepth 1 -type f -name 'pd-wechat-*.sql.gz' -mtime +$((retention_days - 1)) -delete
find "${backup_dir}" -maxdepth 1 -type f -name 'pd-wechat-*.sql.gz.sha256' -mtime +$((retention_days - 1)) -delete

echo "mysql backup complete: $(basename -- "${final_file}")"
